// Package notify 提供 ServerChan（Server酱）推送能力。
//
// 工程本身跑在局域网，不需要公网入口；但“出错了要能收到提醒”，
// 借一个公益推送通道转发到微信是最省事且合规的做法。
package notify

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"sipvoice/internal/netutil"
)

// ServerChan 是 Server酱 的推送实现。
type ServerChan struct {
	Key         string
	MinInterval time.Duration
	HTTP        *http.Client

	mu   sync.Mutex
	last map[string]time.Time
}

// New 创建一个推送器；key 为空时所有发送都会被跳过。
func New(key string, minInterval time.Duration) *ServerChan {
	if minInterval <= 0 {
		minInterval = 5 * time.Minute
	}
	return &ServerChan{
		Key:         key,
		MinInterval: minInterval,
		HTTP: &http.Client{
			Timeout:   15 * time.Second,
			Transport: &http.Transport{DialContext: netutil.DialContext},
		},
		last: map[string]time.Time{},
	}
}

// SetKey 热更新 SendKey 与节流间隔（面板保存配置后调用）。
func (s *ServerChan) SetKey(key string, interval time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Key = strings.TrimSpace(key)
	if interval > 0 {
		s.MinInterval = interval
	}
}

// Enabled 表示是否配置了 Key。
func (s *ServerChan) Enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(s.Key) != ""
}

// Send 推送一条消息。同一 title 在 MinInterval 内只会发一次，避免刷屏。
func (s *ServerChan) Send(ctx context.Context, title, body string) error {
	s.mu.Lock()
	key := strings.TrimSpace(s.Key)
	s.mu.Unlock()
	if key == "" {
		return fmt.Errorf("未配置 ServerChan SendKey")
	}
	if s.throttled(title) {
		return nil
	}
	form := url.Values{}
	form.Set("title", title)
	form.Set("desp", body)
	endpoint := "https://sctapi.ftqq.com/" + key + ".send"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	text := strings.TrimSpace(string(raw))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("ServerChan HTTP %d: %s", resp.StatusCode, text)
	}
	// 正常返回形如 {"code":0,"message":"","data":{"pushid":"...","readkey":"...","error":"SUCCESS","errno":0}}
	if strings.Contains(text, `"code":0`) || strings.Contains(text, `"errno":0`) {
		s.mark(title)
		return nil
	}
	return fmt.Errorf("ServerChan 返回: %s", text)
}

// SendOnce 忽略节流直接推送（用于面板上的“测试推送”）。
func (s *ServerChan) SendOnce(ctx context.Context, title, body string) error {
	s.mu.Lock()
	delete(s.last, title)
	s.mu.Unlock()
	return s.Send(ctx, title, body)
}

func (s *ServerChan) throttled(title string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.last[title]; ok && time.Since(t) < s.MinInterval {
		return true
	}
	return false
}

func (s *ServerChan) mark(title string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last[title] = time.Now()
}
