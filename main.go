package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	// 嵌入 tzdata：容器精简镜像里无需安装 tzdata 包，TZ=Asia/Shanghai 即可生效
	_ "time/tzdata"
)

// Server 聚合配置与 qBittorrent 客户端，供各 HTTP handler 使用。
// 配置与客户端通过 atomic.Pointer 持有，设置页保存后可整体热替换。
type Server struct {
	cfgPath string
	cfgVal  atomic.Pointer[Config]
	qbVal   atomic.Pointer[QBClient]
}

// cfg 返回当前配置（并发安全）
func (s *Server) cfg() *Config { return s.cfgVal.Load() }

// qb 返回当前 qBittorrent 客户端（并发安全）
func (s *Server) qb() *QBClient { return s.qbVal.Load() }

func main() {
	log.SetFlags(log.LstdFlags)
	log.SetPrefix("[qb-stream] ")

	cfg, cfgPath := ParseConfig()

	qb, err := NewQBClient(cfg.QBURL, cfg.QBUser, cfg.QBPass)
	if err != nil {
		log.Fatalf("初始化 qBittorrent 客户端失败: %v", err)
	}
	if err := qb.Login(); err != nil {
		log.Fatalf("%v", err)
	}

	srv := &Server{cfgPath: cfgPath}
	srv.cfgVal.Store(cfg)
	srv.qbVal.Store(qb)

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleSPA)
	mux.HandleFunc("/assets/", handleAssets)
	mux.HandleFunc("/api/torrents", srv.handleAPITorrents)
	mux.HandleFunc("/api/files", srv.handleAPIFiles)
	mux.HandleFunc("/api/add", srv.handleAPIAdd)
	mux.HandleFunc("/api/action", srv.handleAPIAction)
	mux.HandleFunc("/api/settings", srv.handleAPISettings)
	mux.HandleFunc("/stream", srv.handleStream)
	mux.HandleFunc("/playlist", srv.handlePlaylist)

	httpSrv := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Handler: mux,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 内置滚动优先级：领先下载窗口，随进度自动把下一集提为「高」
	go srv.autoPrioLoop(ctx)

	go func() {
		log.Printf("qb-stream 已启动: http://%s:%d（配置文件: %s）", cfg.Host, cfg.Port, cfgPath)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP 服务异常退出: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("收到 SIGINT/SIGTERM，开始优雅关闭...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("优雅关闭超时: %v", err)
	}
	log.Printf("qb-stream 已退出")
}
