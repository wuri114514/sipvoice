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
const DefaultNotifyTo = "you@example.com"

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
}

// DefaultSettings 返回默认设置。
func DefaultSettings() Settings {
	return Settings{
		Primary:  Config{Port: 465, SSL: true},
		Backup:   Config{Port: 465, SSL: true},
		Enabled:  true,
		NotifyTo: DefaultNotifyTo,
		ReplyPDF: true,
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

// Save 保存设置（0600，含授权码）。
func (st *Store) Save(s Settings) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	s.Normalize()
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(st.Path)
	_ = os.MkdirAll(dir, 0o770)
	return os.WriteFile(st.Path, raw, 0o600)
}

// SetLastTest 记录测试结果。
func (st *Store) SetLastTest(msg string, ok bool) {
	s := st.Load()
	s.LastTest = msg
	s.LastOK = ok
	_ = st.Save(s)
}
