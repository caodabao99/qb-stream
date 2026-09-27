package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// videoExts 识别的视频扩展名及对应 Content-Type
var videoExts = map[string]string{
	".mp4":  "video/mp4",
	".mkv":  "video/x-matroska",
	".avi":  "video/x-msvideo",
	".mov":  "video/quicktime",
	".webm": "video/webm",
	".ts":   "video/mp2t",
	".m4v":  "video/x-m4v",
	".flv":  "video/x-flv",
	".wmv":  "video/x-ms-wmv",
}

func isVideoFile(name string) bool {
	_, ok := videoExts[strings.ToLower(path.Ext(name))]
	return ok
}

func contentTypeFor(name string) string {
	if ct, ok := videoExts[strings.ToLower(path.Ext(name))]; ok {
		return ct
	}
	return "application/octet-stream"
}

var errRangeNotSatisfiable = errors.New("range not satisfiable")

// buildCandidatePaths 生成候选本机路径（按可能性排序）。
// qB 下载目录在别人环境里的挂载点不固定（/qbs、/downloads、/data……），不硬编码：
//  1. 手动映射：配置文件 path_map_from → path_map_to（显式指定时最优先）
//  2. 自动探测：qB app/preferences 的 save_path（默认下载目录）→ /media
//  3. 种子自身 save_path 即挂载点：整段剥掉，文件直接落在 /media 下
//  4. 不剥前缀：把 /media 当作包含 qB 路径的父目录挂载
func (s *Server) buildCandidatePaths(savePath, name string) []string {
	to := s.cfg().PathMapTo
	sp := strings.TrimSuffix(savePath, "/")
	seen := map[string]bool{}
	var out []string
	add := func(from string) {
		if from == "" {
			return
		}
		rel := strings.TrimPrefix(sp, strings.TrimSuffix(from, "/"))
		if rel == sp { // save_path 不以 from 开头，该候选无效
			return
		}
		p := path.Join(to, rel, name)
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	add(s.cfg().PathMapFrom)                    // 1) 手动映射（未配置则为空，跳过）
	add(s.qb().DefaultSavePath())               // 2) 自动探测 qB 默认下载目录
	add(sp)                                     // 3) save_path 即挂载点 → rel 为空
	if p := path.Join(to, sp, name); !seen[p] { // 4) 不剥前缀兜底
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// resolvePaths 依次尝试候选路径，每个路径再尝试 qBittorrent 未完成文件的 .!qB 后缀
func resolvePaths(candidates []string) (string, error) {
	var tried []string
	for _, p := range candidates {
		for _, c := range []string{p, p + ".!qB"} {
			if _, err := os.Stat(c); err == nil {
				return c, nil
			}
			tried = append(tried, c)
		}
	}
	return "", fmt.Errorf("文件不存在，已尝试: %s", strings.Join(tried, " ； "))
}

// parseRange 手动解析单区间 Range 头。
// ok=false 表示无/非法/多区间 Range（应按 200 全量返回，符合 RFC 7233 对忽略 Range 的许可）；
// err 仅在区间不可满足（应返回 416）时非空。
func parseRange(spec string, size int64) (start, end int64, ok bool, err error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || !strings.HasPrefix(spec, "bytes=") || strings.Contains(spec, ",") {
		return 0, 0, false, nil
	}
	spec = strings.TrimPrefix(spec, "bytes=")
	dash := strings.IndexByte(spec, '-')
	if dash < 0 {
		return 0, 0, false, nil
	}
	left, right := strings.TrimSpace(spec[:dash]), strings.TrimSpace(spec[dash+1:])

	switch {
	case left == "": // bytes=-500：末尾 500 字节
		n, perr := strconv.ParseInt(right, 10, 64)
		if perr != nil || n <= 0 {
			return 0, 0, false, nil
		}
		if n > size {
			n = size
		}
		return size - n, size - 1, true, nil

	case right == "": // bytes=100-：从 100 到文件尾
		s, perr := strconv.ParseInt(left, 10, 64)
		if perr != nil || s < 0 {
			return 0, 0, false, nil
		}
		if s >= size {
			return 0, 0, true, errRangeNotSatisfiable
		}
		return s, size - 1, true, nil

	default: // bytes=100-200：闭区间
		s, perr := strconv.ParseInt(left, 10, 64)
		if perr != nil || s < 0 {
			return 0, 0, false, nil
		}
		e, perr := strconv.ParseInt(right, 10, 64)
		if perr != nil || e < s {
			return 0, 0, false, nil
		}
		if s >= size {
			return 0, 0, true, errRangeNotSatisfiable
		}
		if e >= size {
			e = size - 1
		}
		return s, e, true, nil
	}
}

// handlePlaylist 处理 GET /playlist?hash=xxx：输出 M3U 剧集播放列表。
// 播放器打开该地址即可在播放器内选集、播完自动下一集。
func (s *Server) handlePlaylist(w http.ResponseWriter, r *http.Request) {
	hash := r.URL.Query().Get("hash")
	if hash == "" {
		http.Error(w, "缺少 hash 参数", http.StatusBadRequest)
		return
	}
	files, err := s.qb().TorrentFiles(hash)
	if err != nil {
		http.Error(w, "获取文件列表失败: "+err.Error(), http.StatusBadGateway)
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Index < files[j].Index })

	base := "http://" + r.Host + "/stream?hash=" + hash + "&file="

	w.Header().Set("Content-Type", "audio/x-mpegurl; charset=utf-8")
	fmt.Fprintln(w, "#EXTM3U")
	n := 0
	for _, f := range files {
		if !isVideoFile(f.Name) {
			continue
		}
		title := strings.TrimSuffix(path.Base(f.Name), path.Ext(f.Name))
		fmt.Fprintf(w, "#EXTINF:-1,%s\n%s%d\n", title, base, f.Index)
		n++
	}
	log.Printf("生成播放列表: hash=%s 共 %d 个视频", shortHash(hash), n)
}

// handleStream 处理 GET /stream?hash=xxx&file=N：等待数据就绪后以 Range 方式流出视频文件
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	hash := q.Get("hash")
	fileIdx, err := strconv.Atoi(q.Get("file"))
	if hash == "" || err != nil || fileIdx < 0 {
		http.Error(w, "参数错误: 需要 hash 与 file（文件序号）", http.StatusBadRequest)
		return
	}

	files, err := s.qb().TorrentFiles(hash)
	if err != nil {
		http.Error(w, "获取文件列表失败: "+err.Error(), http.StatusBadGateway)
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Index < files[j].Index })

	var target *TorrentFile
	var fileOffset int64 // 目标文件之前所有文件 size 之和
	for i := range files {
		if files[i].Index == fileIdx {
			target = &files[i]
			break
		}
		fileOffset += files[i].Size
	}
	if target == nil {
		http.Error(w, fmt.Sprintf("文件序号 %d 不存在", fileIdx), http.StatusNotFound)
		return
	}
	size := target.Size
	if size <= 0 {
		w.Header().Set("Content-Type", contentTypeFor(target.Name))
		w.WriteHeader(http.StatusOK)
		return
	}

	torrents, err := s.qb().TorrentsInfo(hash)
	if err != nil {
		http.Error(w, "获取种子信息失败: "+err.Error(), http.StatusBadGateway)
		return
	}
	var t *Torrent
	for i := range torrents {
		if strings.EqualFold(torrents[i].Hash, hash) {
			t = &torrents[i]
			break
		}
	}
	if t == nil {
		http.Error(w, "未找到对应种子", http.StatusNotFound)
		return
	}

	props, err := s.qb().TorrentProperties(hash)
	if err != nil {
		http.Error(w, "获取种子属性失败: "+err.Error(), http.StatusBadGateway)
		return
	}
	if props.PieceSize <= 0 {
		http.Error(w, fmt.Sprintf("piece_size 非法: %d", props.PieceSize), http.StatusBadGateway)
		return
	}

	// 路径解析：候选映射逐一尝试 + .!qB 未完成后缀回退（路径映射全自动，无需配置）
	realPath, err := resolvePaths(s.buildCandidatePaths(t.SavePath, target.Name))
	if err != nil {
		log.Printf("路径解析失败: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	start, end, hasRange, rerr := parseRange(r.Header.Get("Range"), size)
	if rerr != nil {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
		http.Error(w, "请求区间不可满足", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	length := size
	if hasRange {
		length = end - start + 1
	}

	log.Printf("流请求: hash=%s file=%d %s range=%q → %s [%d-%d] 共 %d 字节",
		shortHash(hash), fileIdx, target.Name, r.Header.Get("Range"), realPath, start, end, length)

	// 等待策略：响应前只等「起始窗口」（约 2 个 piece，至少 8MB）；
	// 开放区间/全量的剩余部分由 streamFile 在跨越未下载 piece 时逐块等待，
	// 从而支持边下边看，无需等整个文件下载完。
	var tracker *pieceTracker
	if target.Progress < 1 {
		tracker = newPieceTracker(s.qb(), hash, props.PieceSize, time.Duration(s.cfg().Poll), time.Duration(s.cfg().MaxWait))
	}
	waitEnd := start + length - 1
	if !hasRange || end == size-1 {
		window := 2 * props.PieceSize
		if window < 8<<20 {
			window = 8 << 20
		}
		if start+window-1 < waitEnd {
			waitEnd = start + window - 1
		}
	}
	if tracker != nil {
		if err := tracker.ensureReady(r.Context(),
			(fileOffset+start)/props.PieceSize, (fileOffset+waitEnd)/props.PieceSize); err != nil {
			http.Error(w, "数据尚未就绪: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
	}

	f, err := os.Open(realPath)
	if err != nil {
		log.Printf("打开文件失败: %v", err)
		http.Error(w, "打开文件失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		http.Error(w, "Seek 失败: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", contentTypeFor(target.Name))
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	if hasRange {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}

	streamFile(w, r, f, length, s.cfg().Chunk, tracker, fileOffset, props.PieceSize, start, hash, fileIdx)
}

// streamFile 分块读取并写出；tracker 非空时，跨越未下载 piece 前阻塞等待（边下边看），
// 客户端断开或单段等待超时则提前结束连接
func streamFile(w http.ResponseWriter, r *http.Request, f *os.File, length int64, chunk int,
	tracker *pieceTracker, fileOffset, pieceSize, start int64, hash string, fileIdx int) {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, chunk)
	pos := start
	remaining := length
	for remaining > 0 {
		if r.Context().Err() != nil {
			log.Printf("客户端断开，停止传输: hash=%s file=%d（剩余 %d 字节）", shortHash(hash), fileIdx, remaining)
			return
		}
		n := int64(len(buf))
		if n > remaining {
			n = remaining
		}
		if tracker != nil {
			first := (fileOffset + pos) / pieceSize
			last := (fileOffset + pos + n - 1) / pieceSize
			if err := tracker.ensureReady(r.Context(), first, last); err != nil {
				log.Printf("流中等待数据失败，提前结束: hash=%s file=%d 位置=%d piece=[%d,%d]: %v",
					shortHash(hash), fileIdx, pos, first, last, err)
				return
			}
		}
		nr, rerr := io.ReadFull(f, buf[:n])
		if nr > 0 {
			if _, werr := w.Write(buf[:nr]); werr != nil {
				log.Printf("写出失败: hash=%s file=%d: %v", shortHash(hash), fileIdx, werr)
				return
			}
			remaining -= int64(nr)
			pos += int64(nr)
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF) {
				log.Printf("文件数据提前结束: hash=%s file=%d 已发送 %d/%d 字节",
					shortHash(hash), fileIdx, length-remaining, length)
			} else {
				log.Printf("读取失败: hash=%s file=%d: %v", shortHash(hash), fileIdx, rerr)
			}
			return
		}
	}
	log.Printf("传输完成: hash=%s file=%d 共 %d 字节", shortHash(hash), fileIdx, length)
}
