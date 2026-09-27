package main

import (
	"context"
	"fmt"
	"log"
	"time"
)

// pieceTracker 维护 pieceStates 缓存，支持响应前等待与流式传输中逐段等待。
// 缓存就绪时零 API 开销；未就绪时按 poll 间隔刷新，直到完成或超时。
type pieceTracker struct {
	qb        *QBClient
	hash      string
	pieceSize int64
	poll      time.Duration
	maxWait   time.Duration
	states    []int
	fetchedAt time.Time
}

func newPieceTracker(qb *QBClient, hash string, pieceSize int64, poll, maxWait time.Duration) *pieceTracker {
	return &pieceTracker{qb: qb, hash: hash, pieceSize: pieceSize, poll: poll, maxWait: maxWait}
}

// ready 判断缓存中 [first,last] 是否全部完成；缓存未覆盖时返回 false
func (t *pieceTracker) ready(first, last int64) bool {
	if int(last) >= len(t.states) {
		return false
	}
	for i := first; i <= last; i++ {
		if t.states[i] != 2 {
			return false
		}
	}
	return true
}

// refresh 拉取最新 pieceStates；无论成败都记录刷新时间，保证轮询节奏恒定
func (t *pieceTracker) refresh() {
	t.fetchedAt = time.Now()
	if states, err := t.qb.PieceStates(t.hash); err == nil {
		t.states = states
	}
}

// ensureReady 阻塞直到 piece 区间 [first,last] 全部下载完成（状态 2）。
// 客户端断开（ctx 取消）或超过 maxWait 时返回错误。
func (t *pieceTracker) ensureReady(ctx context.Context, first, last int64) error {
	if first < 0 || last < first {
		return fmt.Errorf("非法 piece 区间 [%d, %d]", first, last)
	}
	deadline := time.Now().Add(t.maxWait)
	logged := false
	for {
		if t.ready(first, last) {
			if logged {
				log.Printf("数据已就绪: hash=%s piece=[%d,%d]", shortHash(t.hash), first, last)
			}
			return nil
		}
		if !logged {
			log.Printf("等待数据就绪: hash=%s piece=[%d,%d] 超时=%s", shortHash(t.hash), first, last, t.maxWait)
			logged = true
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待数据就绪超时（%s）: hash=%s piece=[%d,%d]",
				t.maxWait, shortHash(t.hash), first, last)
		}
		if time.Since(t.fetchedAt) >= t.poll {
			t.refresh()
		} else {
			select {
			case <-ctx.Done():
				return fmt.Errorf("客户端已断开: %w", ctx.Err())
			case <-time.After(t.poll):
			}
		}
	}
}

func shortHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}
