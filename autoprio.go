package main

import (
	"context"
	"log"
	"strings"
	"time"
)

// qBittorrent 文件优先级取值（4.6.x）
const (
	prioSkip   = 0
	prioNormal = 1
	prioHigh   = 6
	prioMax    = 7
)

// autoPrioLoop 周期性维护滚动下载优先级：
// 对每个下载中的多种子任务，找到第一个未完成的文件 cur（当前观看位置），
// 将 (cur, cur+PrioLead] 中仍为「正常」的文件提升为「高」。
// 即：E01 完成后自动提 E03；E02 完成后提 E04……始终保持领先下载窗口。
// 只升不降，不触碰用户手动设置的跳过/高/最高。
func (s *Server) autoPrioLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(s.cfg().PrioInterval))
	defer ticker.Stop()
	log.Printf("自动优先级已启用: 领先 %d 集，检查间隔 %s", s.cfg().PrioLead, time.Duration(s.cfg().PrioInterval))
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.applyAutoPrio()
		}
	}
}

func (s *Server) applyAutoPrio() {
	torrents, err := s.qb().TorrentsInfo("")
	if err != nil {
		log.Printf("自动优先级: 获取种子列表失败: %v", err)
		return
	}
	for _, t := range torrents {
		// 跳过暂停/停止/出错的任务
		if strings.HasPrefix(t.State, "paused") || strings.HasPrefix(t.State, "stopped") ||
			strings.HasPrefix(t.State, "error") || t.State == "missingFiles" {
			continue
		}
		files, err := s.qb().TorrentFiles(t.Hash)
		if err != nil {
			log.Printf("自动优先级: %s 获取文件失败: %v", shortHash(t.Hash), err)
			continue
		}
		if len(files) <= 1 {
			continue // 单文件任务无需滚动提权
		}
		cur := -1
		for _, f := range files {
			if f.Progress < 1 {
				cur = f.Index
				break
			}
		}
		if cur < 0 {
			continue // 全部完成
		}
		for _, f := range files {
			if f.Index > cur && f.Index <= cur+s.cfg().PrioLead && f.Priority == prioNormal {
				if err := s.qb().SetFilePriority(t.Hash, []int{f.Index}, prioHigh); err != nil {
					log.Printf("自动优先级: %s file=%d 提权失败: %v", shortHash(t.Hash), f.Index, err)
				} else {
					log.Printf("自动优先级: %s file=%d → 高", shortHash(t.Hash), f.Index)
				}
			}
		}
	}
}
