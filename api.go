package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
)

// writeJSON 统一 JSON 输出
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// apiError 输出 {ok:false,error:...}
func apiError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": msg})
}

// torrentView 是 /api/torrents 返回给前端的字段
type torrentView struct {
	Hash          string  `json:"hash"`
	Name          string  `json:"name"`
	Size          int64   `json:"size"`
	Progress      float64 `json:"progress"`
	State         string  `json:"state"`
	StateText     string  `json:"state_text"`
	DLSpeed       int64   `json:"dlspeed"`
	UPSpeed       int64   `json:"upspeed"`
	ETA           int64   `json:"eta"`
	NumSeeds      int     `json:"num_seeds"`
	NumComplete   int     `json:"num_complete"`
	NumLeechs     int     `json:"num_leechs"`
	NumIncomplete int     `json:"num_incomplete"`
	Ratio         float64 `json:"ratio"`
	AddedOn       int64   `json:"added_on"`
	CompletionOn  int64   `json:"completion_on"`
	Tracker       string  `json:"tracker"`
}

// handleAPITorrents GET /api/torrents：种子列表（JSON）
func (s *Server) handleAPITorrents(w http.ResponseWriter, r *http.Request) {
	torrents, err := s.qb().TorrentsInfo("")
	if err != nil {
		apiError(w, http.StatusBadGateway, "获取种子列表失败: "+err.Error())
		return
	}
	sort.Slice(torrents, func(i, j int) bool { return torrents[i].Name < torrents[j].Name })
	views := make([]torrentView, 0, len(torrents))
	for _, t := range torrents {
		views = append(views, torrentView{
			Hash: t.Hash, Name: t.Name, Size: t.Size,
			Progress: t.Progress, State: t.State,
			StateText: stateText(t.State),
			DLSpeed:   t.DLSpeed, UPSpeed: t.UPSpeed, ETA: t.ETA,
			NumSeeds: t.NumSeeds, NumComplete: t.NumComplete,
			NumLeechs: t.NumLeechs, NumIncomplete: t.NumIncomplete,
			Ratio: t.Ratio, AddedOn: t.AddedOn, CompletionOn: t.CompletionOn,
			Tracker: t.Tracker,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"torrents": views,
		"qb_url":   s.cfg().QBURL,
	})
}

// fileView 是 /api/files 返回给前端的字段
type fileView struct {
	Index        int     `json:"index"`
	Name         string  `json:"name"`
	Size         int64   `json:"size"`
	Progress     float64 `json:"progress"`
	Priority     int     `json:"priority"`
	PriorityText string  `json:"priority_text"`
	IsVideo      bool    `json:"is_video"`
}

// handleAPIFiles GET /api/files?hash=xxx：文件列表（JSON）
func (s *Server) handleAPIFiles(w http.ResponseWriter, r *http.Request) {
	hash := r.URL.Query().Get("hash")
	if hash == "" {
		apiError(w, http.StatusBadRequest, "缺少 hash 参数")
		return
	}
	files, err := s.qb().TorrentFiles(hash)
	if err != nil {
		apiError(w, http.StatusBadGateway, "获取文件列表失败: "+err.Error())
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Index < files[j].Index })

	// 取种子名用于页面标题（失败不阻塞）
	torrentName := ""
	if torrents, err := s.qb().TorrentsInfo(hash); err == nil {
		for _, t := range torrents {
			if strings.EqualFold(t.Hash, hash) {
				torrentName = t.Name
				break
			}
		}
	}

	views := make([]fileView, 0, len(files))
	for _, f := range files {
		views = append(views, fileView{
			Index: f.Index, Name: f.Name, Size: f.Size,
			Progress: f.Progress, Priority: f.Priority,
			PriorityText: prioText(f.Priority), IsVideo: isVideoFile(f.Name),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"hash":         hash,
		"torrent_name": torrentName,
		"files":        views,
	})
}

// handleAPIAdd POST /api/add（multipart）：添加种子，默认顺序下载 + 优先头尾 + 立即开始
func (s *Server) handleAPIAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiError(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		apiError(w, http.StatusBadRequest, "解析表单失败: "+err.Error())
		return
	}

	var data []byte
	filename := ""
	if f, fh, err := r.FormFile("torrent"); err == nil {
		defer f.Close()
		if data, err = io.ReadAll(f); err != nil {
			apiError(w, http.StatusBadRequest, "读取 .torrent 文件失败: "+err.Error())
			return
		}
		filename = fh.Filename
	}
	magnet := strings.TrimSpace(r.FormValue("magnet"))
	if len(data) == 0 && magnet == "" {
		apiError(w, http.StatusBadRequest, "请选择 .torrent 文件或填写磁力链接")
		return
	}

	if err := s.qb().AddTorrent(magnet, data, filename); err != nil {
		log.Printf("添加种子失败: %v", err)
		apiError(w, http.StatusBadGateway, "添加失败: "+err.Error())
		return
	}
	log.Printf("添加种子成功: file=%q magnet=%q", filename, truncate(magnet, 60))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": "添加成功（顺序下载 + 优先头尾 + 立即开始）",
	})
}

// handleAPIAction POST /api/action {hash, op}：开始/暂停/删除/删除+文件
func (s *Server) handleAPIAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Hash string `json:"hash"`
		Op   string `json:"op"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
		apiError(w, http.StatusBadRequest, "解析请求失败: "+err.Error())
		return
	}
	if in.Hash == "" {
		apiError(w, http.StatusBadRequest, "缺少 hash")
		return
	}

	var err error
	switch in.Op {
	case "start":
		err = s.qb().StartTorrent(in.Hash)
	case "stop":
		err = s.qb().StopTorrent(in.Hash)
	case "delete":
		err = s.qb().DeleteTorrent(in.Hash, false)
	case "deletefiles":
		err = s.qb().DeleteTorrent(in.Hash, true)
	default:
		apiError(w, http.StatusBadRequest, "未知操作: "+in.Op)
		return
	}
	if err != nil {
		log.Printf("操作 %s 失败: hash=%s: %v", in.Op, shortHash(in.Hash), err)
		apiError(w, http.StatusBadGateway, "操作失败: "+err.Error())
		return
	}
	log.Printf("操作成功: %s hash=%s", in.Op, shortHash(in.Hash))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
