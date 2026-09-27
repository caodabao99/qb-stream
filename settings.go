package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// handleAPISettings GET 返回当前配置（qb_pass 不回传，只回传 has_pass）；
// POST 校验并保存配置。除监听地址/端口（需重启）外，其余修改保存后即时生效。
func (s *Server) handleAPISettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		c := s.cfg()
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":       true,
			"cfg_path": s.cfgPath,
			"has_pass": c.QBPass != "",
			"config": map[string]any{
				"qb_url":        c.QBURL,
				"qb_user":       c.QBUser,
				"host":          c.Host,
				"port":          c.Port,
				"poll":          c.Poll.String(),
				"max_wait":      c.MaxWait.String(),
				"chunk":         c.Chunk,
				"prio_lead":     c.PrioLead,
				"prio_interval": c.PrioInterval.String(),
			},
		})
	case http.MethodPost:
		s.saveAPISettings(w, r)
	default:
		apiError(w, http.StatusMethodNotAllowed, "仅支持 GET/POST")
	}
}

// settingsIn 是前端提交的设置项；qb_pass 为空表示保持不变。
// poll/max_wait/prio_interval 为 "1s"、"30s" 格式的时长字符串（Duration 反序列化）。
type settingsIn struct {
	QBURL        string   `json:"qb_url"`
	QBUser       string   `json:"qb_user"`
	QBPass       string   `json:"qb_pass"`
	Host         string   `json:"host"`
	Port         int      `json:"port"`
	Poll         Duration `json:"poll"`
	MaxWait      Duration `json:"max_wait"`
	Chunk        int      `json:"chunk"`
	PrioLead     int      `json:"prio_lead"`
	PrioInterval Duration `json:"prio_interval"`
}

// saveAPISettings 校验全部通过后先验证 qBittorrent 登录，再写配置文件并热替换
func (s *Server) saveAPISettings(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		apiError(w, http.StatusBadRequest, "读取请求失败: "+err.Error())
		return
	}
	var in settingsIn
	if err := json.Unmarshal(body, &in); err != nil {
		apiError(w, http.StatusBadRequest, "解析请求失败: "+err.Error())
		return
	}

	bad := func(msg string) { apiError(w, http.StatusBadRequest, msg) }
	c := *s.cfg() // 复制当前配置，校验全部通过后才替换
	c.QBURL = strings.TrimRight(strings.TrimSpace(in.QBURL), "/")
	c.QBUser = strings.TrimSpace(in.QBUser)
	if in.QBPass != "" {
		c.QBPass = in.QBPass
	}
	// 路径映射不在设置页展示（自动探测 qB 默认下载目录），保持现有值不变
	c.Host = strings.TrimSpace(in.Host)

	if in.Port < 1 || in.Port > 65535 {
		bad("端口非法（1-65535）")
		return
	}
	c.Port = in.Port

	if in.Poll < Duration(500*time.Millisecond) {
		bad("轮询间隔非法（不低于 500ms，如 1s）")
		return
	}
	c.Poll = in.Poll

	if in.MaxWait <= 0 {
		bad("等待超时非法（如 2m、120s）")
		return
	}
	c.MaxWait = in.MaxWait

	if in.Chunk <= 0 {
		bad("传输块大小非法（正整数字节）")
		return
	}
	c.Chunk = in.Chunk

	if in.PrioLead < 1 {
		bad("领先集数非法（不小于 1）")
		return
	}
	c.PrioLead = in.PrioLead

	if in.PrioInterval < Duration(time.Second) {
		bad("优先级检查间隔非法（不低于 1s，如 30s）")
		return
	}
	c.PrioInterval = in.PrioInterval
	normalizeConfig(&c)

	// 先验证 qBittorrent 连接可用，避免把错误配置落盘
	qbNew, err := NewQBClient(c.QBURL, c.QBUser, c.QBPass)
	if err != nil {
		bad("初始化 qBittorrent 客户端失败: " + err.Error())
		return
	}
	if err := qbNew.Login(); err != nil {
		bad("保存失败: 无法登录 qBittorrent（配置未改动）: " + err.Error())
		return
	}
	if err := saveConfig(s.cfgPath, &c); err != nil {
		apiError(w, http.StatusInternalServerError, "写入配置文件失败: "+err.Error())
		return
	}

	s.cfgVal.Store(&c)
	s.qbVal.Store(qbNew)
	log.Printf("配置已更新并保存: %s", s.cfgPath)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
