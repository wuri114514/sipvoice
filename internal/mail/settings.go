package mail

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// 全局默认收件人：分机没填邮箱时用这个
const DefaultNotifyTo = ""

// Store 保存邮件配置（主 + 备用）。
type Store struct {
	mu   sync.Mutex
	Path string
}

// Settings 是发信设置。
type Settings struct {
	Primary  Config `json:"primary"`
	Backup   Config `json:"backup"`
	Enabled  bool   `json:"enabled"`
	NotifyTo string `json:"notify_to"` // 全局默认收件人
	ReplyPDF bool   `json:"reply_pdf"` // 附件用 PDF（否则用 TIFF）
	LastTest string `json:"last_test"` // 最近一次测试结果
	LastOK   bool   `json:"last_ok"`
	// Receive 是「收信转传真」的收信设置
	Receive ReceiveConfig `json:"receive"`
}

// DefaultSettings 返回默认设置。
func DefaultSettings() Settings {
	return Settings{
		Primary:  Config{Port: 465, SSL: true},
		Backup:   Config{Port: 465, SSL: true},
		Enabled:  true,
		NotifyTo: DefaultNotifyTo,
		ReplyPDF: true,
		Receive:  DefaultReceiveConfig(),
	}
}

// Normalize 清理字段。
func (s *Settings) Normalize() {
	s.Primary.Normalize()
	s.Backup.Normalize()
	s.NotifyTo = strings.TrimSpace(s.NotifyTo)
	if s.NotifyTo == "" {
		s.NotifyTo = DefaultNotifyTo
	}
	s.Receive.Normalize()
}

// SendWithFallback 先主后备，任一成功即返回。
func SendWithFallback(s Settings, to []string, subject, body string, atts []Attach) (string, error) {
	if !s.Enabled {
		return "", fmt.Errorf("邮件通知未启用")
	}
	var lastErr error
	if s.Primary.Ready() {
		if via, err := Send(s.Primary, to, subject, body, atts); err == nil {
			return "主邮局 " + via, nil
		} else {
			lastErr = fmt.Errorf("主邮局: %w", err)
		}
	}
	if s.Backup.Ready() {
		if via, err := Send(s.Backup, to, subject, body, atts); err == nil {
			return "备用邮局 " + via, nil
		} else {
			lastErr = fmt.Errorf("备用邮局: %w", err)
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用的邮局配置")
	}
	return "", lastErr
}

// Load 从文件读取设置。
func (st *Store) Load() Settings {
	st.mu.Lock()
	defer st.mu.Unlock()
	s := DefaultSettings()
	if raw, err := os.ReadFile(st.Path); err == nil {
		_ = json.Unmarshal(raw, &s)
	}
	s.Normalize()
	return s
}

// Save 原子写入设置（先写临时文件再 rename）。
//
// /etc/sipvoice 是 root:sipvoice 2770，而 mail.json 可能是 install 时由 root 建的
// 0640 文件。服务以 sipvoice 身份运行时，直接 os.WriteFile 覆盖会被 permission denied；
// 改成「目录内建临时文件 + rename」只需要目录可写，顺便也避免了
// 面板保存与后台轮询同时写导致 JSON 被截断。
func (st *Store) Save(s Settings) (err error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	s.Normalize()
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(st.Path)
	_ = os.MkdirAll(dir, 0o770)

	tmp, err := os.CreateTemp(dir, ".mail-*.json")
	if err != nil {
		// 目录也写不了的话，退回到直接覆盖（老行为）
		return os.WriteFile(st.Path, raw, 0o600)
	}
	name := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(name)
		}
	}()
	if _, err = tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	_ = os.Chmod(name, 0o600)
	return os.Rename(name, st.Path)
}

// SetLastTest 记录测试结果。
func (st *Store) SetLastTest(msg string, ok bool) {
	s := st.Load()
	s.LastTest = msg
	s.LastOK = ok
	_ = st.Save(s)
}

// SetReceiveStatus 记录一次收信轮询的结果（面板只读展示）。
func (st *Store) SetReceiveStatus(polledAt, result string) {
	s := st.Load()
	s.Receive.LastPoll = polledAt
	s.Receive.LastResult = result
	_ = st.Save(s)
}
