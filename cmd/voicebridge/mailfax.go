package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"sipvoice/internal/fax"
	"sipvoice/internal/mail"
)

// mailFaxWatcher 轮询「收信转传真（Email to Fax）」邮箱。
//
// 触发条件（三条都满足才发传真）：
//   - 邮件未读；
//   - 主题带 [FAX]；
//   - 发件人在白名单里。
//
// 目标号码取正文里的 [号码]，附件里的图片/PDF 经黑白化处理后合成一份多页 TIFF，
// 再挂到 worker 的发传真任务上（复用现有串行发送、失败重试与归档逻辑）。
type mailFaxWatcher struct {
	store *mail.Store
	queue func(dest, file string) error

	mu   sync.Mutex
	last time.Time

	// done 记录「已经处理过的邮件 UID」，兜住标记已读失败的情况：
	// IMAP 的 \Seen 偶发没有落地时，同一封邮件会在下一轮被重新取到，
	// 没有这层去重就会重复发同一份传真。
	done map[int]time.Time
}

// startMailFaxWatcher 启动后台轮询。每 15 秒检查一次配置，按配置里的间隔真正收信，
// 这样在面板改完设置不用重启服务。
func startMailFaxWatcher(ctx context.Context, store *mail.Store, queue func(dest, file string) error) *mailFaxWatcher {
	w := &mailFaxWatcher{store: store, queue: queue, done: map[int]time.Time{}}
	go w.run(ctx)
	return w
}

// alreadyDone 判断这封邮件最近是否已经处理过，并顺手清理过期记录。
func (w *mailFaxWatcher) alreadyDone(uid int) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	// 只保留最近两小时的处理记录，避免内存无限增长
	cutoff := time.Now().Add(-2 * time.Hour)
	for k, t := range w.done {
		if t.Before(cutoff) {
			delete(w.done, k)
		}
	}
	if t, ok := w.done[uid]; ok && time.Since(t) < 2*time.Hour {
		return true
	}
	return false
}

// markDone 记下已处理的邮件。
func (w *mailFaxWatcher) markDone(uid int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done == nil {
		w.done = map[int]time.Time{}
	}
	w.done[uid] = time.Now()
}

func (w *mailFaxWatcher) run(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.poll(ctx)
		}
	}
}

func (w *mailFaxWatcher) poll(ctx context.Context) {
	r := w.store.Load().Receive
	if !r.Enabled || !r.Ready() {
		return
	}
	if len(r.FromAllow) == 0 {
		// 白名单为空时任何邮件都不该被当成传真请求，静默跳过
		return
	}

	w.mu.Lock()
	if time.Since(w.last) < time.Duration(r.PollSeconds)*time.Second {
		w.mu.Unlock()
		return
	}
	w.last = time.Now()
	w.mu.Unlock()

	if ctx.Err() != nil {
		return
	}

	msgs, scanned, err := mail.FetchFaxCandidates(r, 100)
	now := time.Now().Format("2006-01-02 15:04:05")
	if err != nil {
		log.Printf("[mailfax] 收信失败: %v", err)
		w.store.SetReceiveStatus(now, "收信失败: "+err.Error())
		return
	}
	if scanned == 0 {
		log.Printf("[mailfax] 轮询：没有未读邮件")
		w.store.SetReceiveStatus(now, "没有未读邮件")
		return
	}

	// 只打印命中规则的邮件；未命中的每轮都会重新扫头部，不打日志免得刷屏
	log.Printf("[mailfax] 轮询：未读 %d 封，命中 %d 封（白名单 %v，关键字 %q）",
		scanned, len(msgs), r.FromAllow, r.SubjectTag)
	for _, m := range msgs {
		log.Printf("[mailfax]   命中 uid=%d 主题=%q 发件人=%q 附件=%d",
			m.UID, m.Subject, m.From, len(m.Attachments))
	}
	if len(msgs) == 0 {
		w.store.SetReceiveStatus(now, fmt.Sprintf("检查 %d 封未读，无 [FAX] 邮件", scanned))
		return
	}

	var handled []int
	var sentTo []string
	var skipped []string
	for _, m := range msgs {
		if w.alreadyDone(m.UID) {
			// 上一轮已经处理过。通常是 IMAP 的 \Seen 没落地，这里补标一次，
			// 避免同一份传真被重复发出去。
			log.Printf("[mailfax]   跳过 uid=%d：本进程已处理过（补标已读）", m.UID)
			handled = append(handled, m.UID)
			continue
		}
		if !r.SubjectIsFax(m.Subject) {
			// 普通邮件：不碰，保持未读
			log.Printf("[mailfax]   跳过 uid=%d：主题不含 %q", m.UID, r.SubjectTag)
			continue
		}
		if !r.SenderAllowed(m.From) {
			log.Printf("[mailfax] 跳过：发件人 %s 不在白名单（主题 %q）", m.From, m.Subject)
			handled = append(handled, m.UID)
			skipped = append(skipped, "发件人不在白名单")
			continue
		}
		dest := mail.ExtractDest(m.Body)
		if dest == "" {
			log.Printf("[mailfax] 跳过：正文里没有 [号码]（uid=%d 主题 %q）", m.UID, m.Subject)
			handled = append(handled, m.UID)
			skipped = append(skipped, "正文没有 [号码]")
			continue
		}
		if len(m.Attachments) == 0 {
			log.Printf("[mailfax] 跳过：没有图片附件（uid=%d 主题 %q）", m.UID, m.Subject)
			handled = append(handled, m.UID)
			skipped = append(skipped, "没有图片附件")
			continue
		}

		file, err := w.buildFax(m, dest)
		if err != nil {
			log.Printf("[mailfax] 生成传真失败（uid=%d → %s）: %v", m.UID, dest, err)
			handled = append(handled, m.UID)
			skipped = append(skipped, "图片转换失败: "+err.Error())
			continue
		}
		if err := w.queue(dest, file); err != nil {
			// 入队失败就保持未读，下一轮重试
			log.Printf("[mailfax] 排队失败（→ %s）: %v", dest, err)
			continue
		}
		log.Printf("[mailfax] 已排入发送队列 → %s（%s，%d 张图，来源 %s）",
			dest, file, len(m.Attachments), m.From)
		handled = append(handled, m.UID)
		sentTo = append(sentTo, dest)
		w.markDone(m.UID)
	}

	if len(handled) > 0 {
		if err := mail.MarkSeen(r, handled); err != nil {
			log.Printf("[mailfax] 标记已读失败: %v", err)
		}
	}

	status := fmt.Sprintf("检查 %d 封未读", len(msgs))
	if len(sentTo) > 0 {
		status += fmt.Sprintf("，已转传真 → %v", sentTo)
	}
	if len(skipped) > 0 {
		status += fmt.Sprintf("，跳过 %d 封（%s）", len(skipped), skipped[0])
	}
	w.store.SetReceiveStatus(now, status)
}

// buildFax 把邮件里的图片附件合成一份 TIFF，落在传真 spool 目录。
func (w *mailFaxWatcher) buildFax(m mail.Incoming, dest string) (string, error) {
	dir, err := os.MkdirTemp("", "sipvoice-mail-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)

	files := make([]string, 0, len(m.Attachments))
	for i, a := range m.Attachments {
		name := filepath.Base(strings.TrimSpace(a.Name))
		if name == "" || name == "." || name == "/" {
			name = fmt.Sprintf("att-%d", i+1)
		}
		p := filepath.Join(dir, fmt.Sprintf("%02d-%s", i+1, name))
		if err := os.WriteFile(p, a.Data, 0o644); err != nil {
			return "", fmt.Errorf("写入附件失败: %w", err)
		}
		files = append(files, p)
	}

	cfg := fax.Load()
	name := fmt.Sprintf("mail-%s-u%d-to%s.tiff", time.Now().Format("20060102-150405"), m.UID, dest)
	out := filepath.Join(cfg.Spool, name)
	if err := fax.ImageToTIFF(files, out); err != nil {
		return "", err
	}
	_ = os.Chmod(out, 0o664)
	_ = exec.Command("chgrp", "sipvoice", out).Run()
	return filepath.Base(out), nil
}
