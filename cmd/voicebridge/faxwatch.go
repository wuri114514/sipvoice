package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"sipvoice/internal/fax"
	"sipvoice/internal/session"
)

// faxWatcher 监控传真存档目录：发现新传真就交给视觉模型分析，
// 并把结果写进传真记录（同名 .txt）和对应会话的历史里；
// 分析完成后按配置决定是否自动回拨。
type faxWatcher struct {
	seen     map[string]bool
	retry    map[string]int
	mu       sync.Mutex
	analyze  func(ctx context.Context, item string) (string, error)
	sessions *session.Manager
	spool    string
	onDone   func(key, summary string) // 分析完成回调（用于自动回拨）
}

// startFaxWatcher 每 20 秒扫一次存档目录。
func startFaxWatcher(ctx context.Context, sm *session.Manager, analyze func(context.Context, string) (string, error), onDone func(key, summary string)) {
	w := &faxWatcher{
		seen:     map[string]bool{},
		retry:    map[string]int{},
		analyze:  analyze,
		sessions: sm,
		spool:    fax.Load().Spool,
		onDone:   onDone,
	}
	// 首次运行把已有文件标记为已处理，避免重启后把历史传真全部重跑一遍
	_ = filepath.Walk(w.spool, func(p string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			w.seen[p] = true
		}
		return nil
	})

	go func() {
		tick := time.NewTicker(20 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				w.scan(ctx)
			}
		}
	}()
}

// scan 找出新传真并分析。
func (w *faxWatcher) scan(ctx context.Context) {
	entries, err := os.ReadDir(w.spool)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		low := strings.ToLower(e.Name())
		if !strings.HasSuffix(low, ".tiff") && !strings.HasSuffix(low, ".tif") {
			continue
		}
		p := filepath.Join(w.spool, e.Name())
		w.mu.Lock()
		if w.seen[p] {
			w.mu.Unlock()
			continue
		}
		w.seen[p] = true
		w.mu.Unlock()

		// 已有分析结果就跳过
		if fax.Analyzed(p) {
			continue
		}

		// 等文件真正写完：轮询大小，连续两次不变才算稳定
		// （Asterisk 收完才落盘，但大文件可能还在 flush；固定 sleep 不够稳）
		if !waitStable(p, 30, 500*time.Millisecond) {
			log.Printf("[faxwatch] %s 文件一直不稳定，本轮跳过", e.Name())
			w.mu.Lock()
			delete(w.seen, p) // 允许下一轮重试
			w.mu.Unlock()
			continue
		}

		log.Printf("[faxwatch] 发现新传真 %s，开始分析", e.Name())
		actx, cancel := context.WithTimeout(ctx, 180*time.Second)
		text, err := w.analyze(actx, e.Name())
		cancel()
		if err != nil {
			log.Printf("[faxwatch] 分析失败 %s: %v", e.Name(), err)
			// 失败不永久放弃：从 seen 里移除，下一轮再试（最多重试 3 次）
			w.mu.Lock()
			w.retry[e.Name()]++
			if w.retry[e.Name()] >= 3 {
				log.Printf("[faxwatch] %s 连续失败 3 次，放弃", e.Name())
			} else {
				delete(w.seen, p)
			}
			w.mu.Unlock()
			continue
		}
		log.Printf("[faxwatch] 分析完成 %s：%s", e.Name(), truncateStr(text, 80))
		// 结果写进会话：优先用文件名里的主叫号码，没有就用最近来电号码兜底
		caller := callerOf(e.Name())
		if caller == "" {
			caller = recentCaller()
		}
		if caller != "" && w.onDone != nil {
			w.onDone(caller, text)
		} else if caller != "" && w.sessions != nil {
			w.sessions.Ensure(caller)
			w.sessions.Append(caller, session.RoleAssistant,
				"（系统提示：你刚收到一页传真，内容是——"+text+"）", "fax")
			log.Printf("[faxwatch] 已写入会话 %s", caller)
		}
	}
}

// callerOf 从文件名里取主叫号码：20261125-073923-1003.tiff → 1003
func callerOf(name string) string {
	base := strings.TrimSuffix(strings.TrimSuffix(name, ".tiff"), ".tif")
	parts := strings.Split(base, "-")
	if len(parts) >= 3 {
		return parts[2]
	}
	return ""
}

func truncateStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// 避免未使用告警
var _ = json.Marshal

// analyzeFaxItem 是 webui 分析逻辑的轻量复制（避免 main 依赖 webui 内部函数）。
// 把传真每页转成 PNG 后逐页交给视觉模型。
func analyzeFaxItem(ctx context.Context, item string) (string, error) {
	cfg := fax.Load()
	pages, err := fax.Preview(item, cfg)
	if err != nil {
		return "", err
	}
	if len(pages) == 0 {
		return "", errNoPages
	}
	var parts []string
	for i, p := range pages {
		text, err := visionFile(ctx, p, fmt.Sprintf("第 %d/%d 页：这是一页传真，请用简洁中文说明要点（金额、日期、人名、编号等关键信息要列出）。", i+1, len(pages)))
		if err != nil {
			return "", err
		}
		parts = append(parts, text)
	}
	// ���果保存到传真旁边
	_ = fax.SaveText(item, strings.Join(parts, "\n\n"))
	return strings.Join(parts, "\n\n"), nil
}

var errNoPages = fmt.Errorf("没有可分析的页面")

// recentCaller 返回最近一通电话的主叫号码（用于传真文件名不含号码时的兜底）。
func recentCaller() string {
	const dir = "/var/lib/sipvoice/callmap"
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return ""
	}
	var latest os.DirEntry
	var latestTime time.Time
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(latestTime) {
			latest = e
			latestTime = info.ModTime()
		}
	}
	if latest == nil || time.Since(latestTime) > 30*time.Minute {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(dir, latest.Name()))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// waitStable 轮询文件大小，直到连续两次读取结果一致（说明写完了）。
func waitStable(p string, maxTries int, interval time.Duration) bool {
	var prev int64 = -1
	for i := 0; i < maxTries; i++ {
		fi, err := os.Stat(p)
		if err != nil {
			return false
		}
		if fi.Size() > 0 && fi.Size() == prev {
			return true
		}
		prev = fi.Size()
		time.Sleep(interval)
	}
	return false
}
