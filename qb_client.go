package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Torrent 是 /api/v2/torrents/info 返回的关键字段
type Torrent struct {
	Hash          string  `json:"hash"`
	Name          string  `json:"name"`
	Size          int64   `json:"size"`
	Progress      float64 `json:"progress"`
	State         string  `json:"state"`
	SavePath      string  `json:"save_path"`
	DLSpeed       int64   `json:"dlspeed"`
	UPSpeed       int64   `json:"upspeed"`
	ETA           int64   `json:"eta"`            // 预计剩余秒数；<0 表示未知/无限制
	NumSeeds      int     `json:"num_seeds"`      // 已连接做种者
	NumComplete   int     `json:"num_complete"`   // swarm 中做种者总数
	NumLeechs     int     `json:"num_leechs"`     // 已连接下载者
	NumIncomplete int     `json:"num_incomplete"` // swarm 中下载者总数
	Ratio         float64 `json:"ratio"`          // 分享率；<0 表示暂无数据
	AddedOn       int64   `json:"added_on"`       // 添加时间（Unix 秒）
	CompletionOn  int64   `json:"completion_on"`  // 完成时间（Unix 秒）；<0 表示尚未完成
	Tracker       string  `json:"tracker"`        // 当前 tracker 地址
}

// TorrentFile 是 /api/v2/torrents/files 返回的关键字段
type TorrentFile struct {
	Index    int     `json:"index"`
	Name     string  `json:"name"`
	Size     int64   `json:"size"`
	Progress float64 `json:"progress"`
	Priority int     `json:"priority"`
}

// TorrentProperties 是 /api/v2/torrents/properties 返回的关键字段
type TorrentProperties struct {
	PieceSize int64 `json:"piece_size"`
	PiecesNum int64 `json:"pieces_num"`
}

// QBClient 封装 qBittorrent WebUI API，SID Cookie 由 cookiejar 自动管理
type QBClient struct {
	baseURL string
	user    string
	pass    string
	http    *http.Client
	// prefsMu/prefsSavePath 缓存 qB 默认下载目录（自动路径映射用）
	prefsMu       sync.Mutex
	prefsSavePath string
}

// NewQBClient 创建带 cookiejar 的客户端
func NewQBClient(baseURL, user, pass string) (*QBClient, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("创建 cookiejar 失败: %w", err)
	}
	return &QBClient{
		baseURL: baseURL,
		user:    user,
		pass:    pass,
		http: &http.Client{
			Jar:     jar,
			Timeout: 30 * time.Second,
		},
	}, nil
}

// Login 登录 qBittorrent，成功后 SID 由 cookiejar 自动携带
func (c *QBClient) Login() error {
	form := url.Values{}
	form.Set("username", c.user)
	form.Set("password", c.pass)
	resp, err := c.http.PostForm(c.baseURL+"/api/v2/auth/login", form)
	if err != nil {
		return fmt.Errorf("登录请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("Ok.")) {
		return fmt.Errorf("登录失败: status=%d body=%s", resp.StatusCode, truncate(string(body), 200))
	}
	log.Printf("已登录 qBittorrent %s（用户: %s）", c.baseURL, c.user)
	return nil
}

// do 发起 GET 请求；收到 403（会话过期）时自动重新登录并重试一次
func (c *QBClient) do(apiPath string, query url.Values) ([]byte, error) {
	full := c.baseURL + apiPath
	if len(query) > 0 {
		full += "?" + query.Encode()
	}
	for attempt := 0; ; attempt++ {
		resp, err := c.http.Get(full)
		if err != nil {
			return nil, fmt.Errorf("请求 %s 失败: %w", apiPath, err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("读取 %s 响应失败: %w", apiPath, err)
		}
		if resp.StatusCode == http.StatusForbidden && attempt == 0 {
			log.Printf("%s 返回 403，尝试重新登录", apiPath)
			if lerr := c.Login(); lerr != nil {
				return nil, lerr
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s 返回 %d: %s", apiPath, resp.StatusCode, truncate(string(body), 200))
		}
		return body, nil
	}
}

// DefaultSavePath 返回 qB 默认下载目录（app/preferences 的 save_path），带缓存。
// 获取失败时返回空串且不缓存，下次调用自动重试。
func (c *QBClient) DefaultSavePath() string {
	c.prefsMu.Lock()
	defer c.prefsMu.Unlock()
	if c.prefsSavePath != "" {
		return c.prefsSavePath
	}
	body, err := c.do("/api/v2/app/preferences", nil)
	if err != nil {
		log.Printf("获取 qB 偏好设置失败（路径自动探测退化）: %v", err)
		return ""
	}
	var p struct {
		SavePath string `json:"save_path"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		log.Printf("解析 qB 偏好设置失败: %v", err)
		return ""
	}
	if p.SavePath == "" {
		return ""
	}
	c.prefsSavePath = p.SavePath
	log.Printf("自动探测 qB 默认下载目录: %s", p.SavePath)
	return p.SavePath
}

// TorrentsInfo 查询种子列表；hashes 为空时返回全部
func (c *QBClient) TorrentsInfo(hashes string) ([]Torrent, error) {
	q := url.Values{}
	if hashes != "" {
		q.Set("hashes", hashes)
	}
	body, err := c.do("/api/v2/torrents/info", q)
	if err != nil {
		return nil, err
	}
	var list []Torrent
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("解析 torrents/info 失败: %w", err)
	}
	return list, nil
}

// TorrentFiles 查询种子内文件列表
func (c *QBClient) TorrentFiles(hash string) ([]TorrentFile, error) {
	body, err := c.do("/api/v2/torrents/files", url.Values{"hash": []string{hash}})
	if err != nil {
		return nil, err
	}
	var list []TorrentFile
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("解析 torrents/files 失败: %w", err)
	}
	return list, nil
}

// TorrentProperties 查询种子属性（piece_size 等）
func (c *QBClient) TorrentProperties(hash string) (*TorrentProperties, error) {
	body, err := c.do("/api/v2/torrents/properties", url.Values{"hash": []string{hash}})
	if err != nil {
		return nil, err
	}
	var p TorrentProperties
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("解析 torrents/properties 失败: %w", err)
	}
	return &p, nil
}

// PieceStates 查询全部 piece 状态：0 未下载 / 1 下载中 / 2 完成
func (c *QBClient) PieceStates(hash string) ([]int, error) {
	body, err := c.do("/api/v2/torrents/pieceStates", url.Values{"hash": []string{hash}})
	if err != nil {
		return nil, err
	}
	var states []int
	if err := json.Unmarshal(body, &states); err != nil {
		return nil, fmt.Errorf("解析 pieceStates 失败: %w", err)
	}
	return states, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// postForm 发起表单 POST 请求；收到 403（会话过期）时自动重新登录并重试一次
func (c *QBClient) postForm(apiPath string, form url.Values) error {
	full := c.baseURL + apiPath
	for attempt := 0; ; attempt++ {
		resp, err := c.http.PostForm(full, form)
		if err != nil {
			return fmt.Errorf("请求 %s 失败: %w", apiPath, err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode == http.StatusForbidden && attempt == 0 {
			log.Printf("%s 返回 403，尝试重新登录", apiPath)
			if lerr := c.Login(); lerr != nil {
				return lerr
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("%s 返回 %d: %s", apiPath, resp.StatusCode, truncate(string(body), 200))
		}
		return nil
	}
}

// AddTorrent 添加种子：磁力链接或 .torrent 文件内容（至少提供一个）。
// 默认启用顺序下载、首尾 piece 优先，并立即开始。
func (c *QBClient) AddTorrent(magnet string, torrentData []byte, filename string) error {
	buildBody := func() (*bytes.Buffer, string, error) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		if len(torrentData) > 0 {
			fw, err := mw.CreateFormFile("torrents", filename)
			if err != nil {
				return nil, "", err
			}
			if _, err := fw.Write(torrentData); err != nil {
				return nil, "", err
			}
		}
		if magnet != "" {
			if err := mw.WriteField("urls", magnet); err != nil {
				return nil, "", err
			}
		}
		for _, kv := range [][2]string{
			{"sequentialDownload", "true"},
			{"firstLastPiecePrio", "true"},
			{"paused", "false"},
		} {
			if err := mw.WriteField(kv[0], kv[1]); err != nil {
				return nil, "", err
			}
		}
		if err := mw.Close(); err != nil {
			return nil, "", err
		}
		return &buf, mw.FormDataContentType(), nil
	}

	const apiPath = "/api/v2/torrents/add"
	for attempt := 0; ; attempt++ {
		buf, ct, err := buildBody()
		if err != nil {
			return fmt.Errorf("构造添加请求失败: %w", err)
		}
		resp, err := c.http.Post(c.baseURL+apiPath, ct, buf)
		if err != nil {
			return fmt.Errorf("添加种子请求失败: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode == http.StatusForbidden && attempt == 0 {
			log.Printf("%s 返回 403，尝试重新登录", apiPath)
			if lerr := c.Login(); lerr != nil {
				return lerr
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("%s 返回 %d: %s", apiPath, resp.StatusCode, truncate(string(body), 200))
		}
		if strings.Contains(string(body), "Fails.") {
			return fmt.Errorf("qBittorrent 拒绝该种子（可能是无效的磁力链接或 .torrent 文件）")
		}
		return nil
	}
}

// StartTorrent 开始/恢复下载。qB 5.x 用 start，4.6.x 用 resume，自动回退。
func (c *QBClient) StartTorrent(hash string) error {
	err := c.postForm("/api/v2/torrents/start", url.Values{"hashes": []string{hash}})
	if err != nil && strings.Contains(err.Error(), "404") {
		return c.postForm("/api/v2/torrents/resume", url.Values{"hashes": []string{hash}})
	}
	return err
}

// StopTorrent 暂停下载。qB 5.x 用 stop，4.6.x 用 pause，自动回退。
func (c *QBClient) StopTorrent(hash string) error {
	err := c.postForm("/api/v2/torrents/stop", url.Values{"hashes": []string{hash}})
	if err != nil && strings.Contains(err.Error(), "404") {
		return c.postForm("/api/v2/torrents/pause", url.Values{"hashes": []string{hash}})
	}
	return err
}

// DeleteTorrent 删除任务；deleteFiles 为 true 时同时删除已下载文件
func (c *QBClient) DeleteTorrent(hash string, deleteFiles bool) error {
	return c.postForm("/api/v2/torrents/delete", url.Values{
		"hashes":      []string{hash},
		"deleteFiles": []string{strconv.FormatBool(deleteFiles)},
	})
}

// SetFilePriority 设置文件优先级（qB 4.6: 0=跳过 1=正常 6=高 7=最高）
func (c *QBClient) SetFilePriority(hash string, ids []int, priority int) error {
	ss := make([]string, len(ids))
	for i, id := range ids {
		ss[i] = strconv.Itoa(id)
	}
	return c.postForm("/api/v2/torrents/filePrio", url.Values{
		"hash":     []string{hash},
		"id":       []string{strings.Join(ss, "|")},
		"priority": []string{strconv.Itoa(priority)},
	})
}
