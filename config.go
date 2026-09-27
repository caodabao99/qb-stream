package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// Duration 是支持 JSON 序列化的 time.Duration，存为 "1s"、"30s" 这类字符串
type Duration time.Duration

func (d Duration) String() string {
	return time.Duration(d).String()
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("解析时长 %q 失败: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

// Config 全部运行配置，持久化为 JSON 配置文件（默认 qb-stream.json）。
// 首次运行由命令行参数引导生成；之后配置文件优先于命令行参数。
type Config struct {
	QBURL        string   `json:"qb_url"`
	QBUser       string   `json:"qb_user"`
	QBPass       string   `json:"qb_pass"`
	PathMapFrom  string   `json:"path_map_from"`
	PathMapTo    string   `json:"path_map_to"`
	Host         string   `json:"host"`
	Port         int      `json:"port"`
	Poll         Duration `json:"poll"`
	MaxWait      Duration `json:"max_wait"`
	Chunk        int      `json:"chunk"`
	PrioLead     int      `json:"prio_lead"`
	PrioInterval Duration `json:"prio_interval"`
}

// defaultConfig 返回默认配置（作为首次运行的引导值）
func defaultConfig() *Config {
	return &Config{
		QBURL:        "http://127.0.0.1:8080",
		QBUser:       "admin",
		PathMapFrom:  "",
		PathMapTo:    "/media",
		Host:         "127.0.0.1",
		Port:         8888,
		Poll:         Duration(time.Second),
		MaxWait:      Duration(120 * time.Second),
		Chunk:        65536,
		PrioLead:     1,
		PrioInterval: Duration(30 * time.Second),
	}
}

// envOr 返回环境变量值（未设置或为空时用默认值），用于 Docker 首次启动引导
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envOrInt 同 envOr，用于整型环境变量
func envOrInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// ParseConfig 决定最终配置：配置文件存在则加载（文件优先于一切），
// 否则按 环境变量 > 命令行参数 构建并立即写入配置文件（Docker 首次启动走环境变量）。
func ParseConfig() (*Config, string) {
	cfgPath := flag.String("config", "qb-stream.json", "配置文件路径")
	cfg := defaultConfig()
	flag.StringVar(&cfg.QBURL, "qb-url", envOr("QBSTREAM_QB_URL", cfg.QBURL), "qBittorrent WebUI 地址")
	flag.StringVar(&cfg.QBUser, "qb-user", envOr("QBSTREAM_QB_USER", cfg.QBUser), "qBittorrent 用户名")
	flag.StringVar(&cfg.QBPass, "qb-pass", envOr("QBSTREAM_QB_PASS", ""), "qBittorrent 密码（首次运行必填，之后存于配置文件）")
	flag.StringVar(&cfg.PathMapFrom, "path-map-from", envOr("QBSTREAM_PATH_MAP_FROM", cfg.PathMapFrom), "qB 容器内路径前缀")
	flag.StringVar(&cfg.PathMapTo, "path-map-to", envOr("QBSTREAM_PATH_MAP_TO", cfg.PathMapTo), "本机挂载路径前缀")
	flag.StringVar(&cfg.Host, "host", envOr("QBSTREAM_HOST", cfg.Host), "监听地址")
	flag.IntVar(&cfg.Port, "port", envOrInt("QBSTREAM_PORT", cfg.Port), "监听端口")
	var poll, maxWait, prioInterval time.Duration
	flag.DurationVar(&poll, "poll", time.Duration(cfg.Poll), "pieceStates 轮询间隔（不低于 500ms）")
	flag.DurationVar(&maxWait, "max-wait", time.Duration(cfg.MaxWait), "等待数据就绪超时")
	flag.IntVar(&cfg.Chunk, "chunk", cfg.Chunk, "传输块大小（字节）")
	flag.IntVar(&cfg.PrioLead, "prio-lead", cfg.PrioLead, "自动优先级：领先当前未完成文件的集数")
	flag.DurationVar(&prioInterval, "prio-interval", time.Duration(cfg.PrioInterval), "自动优先级检查间隔")
	flag.Parse()
	cfg.Poll, cfg.MaxWait, cfg.PrioInterval = Duration(poll), Duration(maxWait), Duration(prioInterval)

	// 配置文件存在则优先使用（环境变量/命令行参数仅在首次生成配置时生效）
	if data, err := os.ReadFile(*cfgPath); err == nil {
		var loaded Config
		if err := json.Unmarshal(data, &loaded); err != nil {
			log.Printf("配置文件 %s 解析失败: %v，改用环境变量/命令行参数", *cfgPath, err)
		} else {
			normalizeConfig(&loaded)
			log.Printf("已加载配置文件: %s（配置文件优先，环境变量/命令行参数已忽略）", *cfgPath)
			return &loaded, *cfgPath
		}
	} else if !os.IsNotExist(err) {
		log.Printf("读取配置文件 %s 失败: %v，改用环境变量/命令行参数", *cfgPath, err)
	}

	// 首次运行：使用命令行参数构建配置
	if cfg.QBPass == "" {
		fmt.Fprintln(os.Stderr, "错误: 必须通过 --qb-pass 提供 qBittorrent 密码（首次运行后密码将存入配置文件）")
		flag.Usage()
		os.Exit(1)
	}
	normalizeConfig(cfg)
	if err := saveConfig(*cfgPath, cfg); err != nil {
		log.Printf("写入配置文件失败: %v", err)
	} else {
		log.Printf("首次运行，已写入配置文件: %s", *cfgPath)
	}
	return cfg, *cfgPath
}

// normalizeConfig 校验并修正配置取值（加载与保存后共用）
func normalizeConfig(c *Config) {
	c.QBURL = strings.TrimRight(c.QBURL, "/")
	if c.QBUser == "" {
		c.QBUser = "admin"
	}
	// path_map_from 留空 = 自动探测 qB 默认下载目录（推荐，适配任意 qB 挂载点）；
	// path_map_to 为 qb-stream 内下载目录挂载点（Docker 固定 /media）
	if c.PathMapTo == "" {
		c.PathMapTo = "/media"
	}
	if c.Host == "" {
		c.Host = "127.0.0.1"
	}
	if c.Port < 1 || c.Port > 65535 {
		c.Port = 8888
	}
	// 轮询间隔不低于 500ms，避免打爆 WebUI API
	if c.Poll < Duration(500*time.Millisecond) {
		c.Poll = Duration(500 * time.Millisecond)
	}
	if c.MaxWait <= 0 {
		c.MaxWait = Duration(120 * time.Second)
	}
	if c.Chunk <= 0 {
		c.Chunk = 65536
	}
	if c.PrioLead < 1 {
		c.PrioLead = 1
	}
	if c.PrioInterval < Duration(time.Second) {
		c.PrioInterval = Duration(time.Second)
	}
}

// saveConfig 以缩进 JSON + 0600 权限写配置文件（含密码，需限制读取权限）
func saveConfig(path string, c *Config) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}
