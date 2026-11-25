package notify

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"sipvoice/internal/mail"
)

// MailChannel 用配置好的邮局把通知发到邮箱。
// 与 ServerChan 并存：两边可以同时开，也可以只开一边。
type MailChannel struct {
	Store       mail.Store
	MinInterval time.Duration

	mu   sync.Mutex
	last map[string]time.Time

	Subject string
	Body    string
	To      string
}

// NewMailChannel 创建邮件通道。
func NewMailChannel(storePath string, minInterval time.Duration) *MailChannel {
	if minInterval <= 0 {
		minInterval = 5 * time.Minute
	}
	return &MailChannel{
		Store:       mail.Store{Path: storePath},
		MinInterval: minInterval,
		last:        map[string]time.Time{},
		Subject:     "[SIPVoice] {title}",
		Body:        "{title}\r\n\r\n{body}\r\n\r\n时间：{time}\r\n主机：{host}",
	}
}

// Enabled 判断邮局是否可用（需要在面板配好发信邮局）。
func (m *MailChannel) Enabled() bool {
	st := m.Store.Load()
	return st.Enabled && (st.Primary.Ready() || st.Backup.Ready())
}

// render 套用模板变量。
func (m *MailChannel) render(tpl, title, body string) string {
	host, _ := os.Hostname()
	if host == "" {
		host = "sipvoice"
	}
	r := strings.NewReplacer(
		"{title}", title,
		"{body}", body,
		"{time}", time.Now().Format("2006-01-02 15:04:05"),
		"{host}", host,
	)
	return r.Replace(tpl)
}

// Send 发送一封通知邮件。
func (m *MailChannel) Send(ctx context.Context, title, body string) error {
	if m.throttled(title) {
		return nil
	}
	st := m.Store.Load()
	if !st.Enabled {
		return fmt.Errorf("邮局通道未启用")
	}
	to := strings.TrimSpace(m.To)
	if to == "" {
		to = st.NotifyTo
	}
	if to == "" {
		to = mail.DefaultNotifyTo
	}
	subject := m.render(m.Subject, title, body)
	content := m.render(m.Body, title, body)

	via, err := mail.SendWithFallback(st, []string{to}, subject, content, nil)
	if err != nil {
		return fmt.Errorf("邮局发送失败: %w", err)
	}
	_ = via
	m.mark(title)
	return nil
}

// SendOnce 忽略节流。
func (m *MailChannel) SendOnce(ctx context.Context, title, body string) error {
	m.mu.Lock()
	delete(m.last, title)
	m.mu.Unlock()
	return m.Send(ctx, title, body)
}

func (m *MailChannel) throttled(title string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.last[title]; ok && time.Since(t) < m.MinInterval {
		return true
	}
	return false
}

func (m *MailChannel) mark(title string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.last[title] = time.Now()
}

// Multi 把多个通道组合成一个：任一成功即算成功，全部失败才报错。
type Multi struct {
	Channels []Sender
}

// Sender 是任意通知通道。
type Sender interface {
	Enabled() bool
	SendOnce(ctx context.Context, title, body string) error
}

// Enabled 只要有一个通道可用就行。
func (m *Multi) Enabled() bool {
	for _, c := range m.Channels {
		if c != nil && c.Enabled() {
			return true
		}
	}
	return false
}

// SendOnce 依次尝试所有可用通道，返回成功的通道名。
func (m *Multi) SendOnce(ctx context.Context, title, body string) (string, error) {
	var (
		ok   []string
		errs []string
	)
	for _, c := range m.Channels {
		if c == nil || !c.Enabled() {
			continue
		}
		if err := c.SendOnce(ctx, title, body); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		ok = append(ok, channelName(c))
	}
	if len(ok) > 0 {
		return strings.Join(ok, " + "), nil
	}
	if len(errs) == 0 {
		return "", fmt.Errorf("没有可用的通知通道")
	}
	return "", fmt.Errorf("%s", strings.Join(errs, "；"))
}

func channelName(c Sender) string {
	switch c.(type) {
	case *ServerChan:
		return "ServerChan"
	case *MailChannel:
		return "邮局"
	}
	return "未知通道"
}
