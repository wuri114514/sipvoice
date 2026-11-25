// Package adapters 提供与 ASR / TTS / AI 服务对接的 HTTP 实现。
// 默认按 OpenAI 兼容接口编写，可用 base URL 指向本地或第三方兼容服务。
package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"sipvoice/internal/netutil"
)

// sharedTransport / sharedClient 让所有云端调用共用连接池：
// 电话场景里识别、对话、合成都打同一个域名，复用 TLS 连接能把首包延迟从数秒降到几百毫秒。
var sharedTransport = &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	DialContext:           netutil.DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          64,
	MaxIdleConnsPerHost:   8,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: time.Second,
}

var sharedClient = &http.Client{Timeout: 180 * time.Second, Transport: sharedTransport}

// Warm 先访问一次 /models，提前建立 DNS/TLS 连接，避免首通电话等十几秒。
func Warm(ctx context.Context, baseURL, apiKey string) {
	base := NormalizeBase(baseURL)
	if base == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if resp, err := sharedClient.Do(req); err == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
	}
}

// Client 是三家服务共用的 HTTP 客户端封装。
type Client struct {
	BaseURL   string // 例如 https://api.openai.com/v1
	APIKey    string
	Model     string
	MaxTokens int // 0 表示不限制
	HTTP      *http.Client
}

// Speech 把默认音色固化，满足 pipeline.Synthesizer 接口。
type Speech struct {
	C     *Client
	Voice string
}

// Speak 实现 pipeline.Synthesizer。
func (s *Speech) Speak(ctx context.Context, text string) ([]byte, error) {
	voice := s.Voice
	if voice == "" {
		voice = "alloy"
	}
	return s.C.Speak(ctx, text, voice)
}

func New(baseURL, apiKey, model string) *Client {
	return &Client{
		BaseURL: NormalizeBase(baseURL),
		APIKey:  apiKey,
		Model:   model,
		HTTP:    sharedClient,
	}
}

// ChatWithHistory 带历史上下文的多轮对话。
// history 是 [{role, content}] 形式的既有对话，会排在 system 之后、本轮 user 之前。
func (c *Client) ChatWithHistory(ctx context.Context, system string, history []map[string]string, user string) (string, error) {
	msgs := make([]map[string]string, 0, len(history)+2)
	if strings.TrimSpace(system) != "" {
		msgs = append(msgs, map[string]string{"role": "system", "content": system})
	}
	msgs = append(msgs, history...)
	msgs = append(msgs, map[string]string{"role": "user", "content": user})

	payload := map[string]any{
		"model":       c.Model,
		"messages":    msgs,
		"temperature": 0.7,
	}
	if c.MaxTokens > 0 {
		payload["max_tokens"] = c.MaxTokens
	}
	body, err := c.postJSON(ctx, "/chat/completions", payload)
	if err != nil {
		return "", err
	}
	var out struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
		Error *struct{ Message string } `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("chat: decode: %w", err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return "", fmt.Errorf("chat: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("chat: empty choices")
	}
	return out.Choices[0].Message.Content, nil
}

// Vision 把图片（data URL）连同问题交给多模态模型。
func (c *Client) Vision(ctx context.Context, prompt, dataURL string) (string, error) {
	payload := map[string]any{
		"model": c.Model,
		"messages": []map[string]any{
			{"role": "user", "content": []map[string]any{
				{"type": "image_url", "image_url": map[string]string{"url": dataURL}},
				{"type": "text", "text": prompt},
			}},
		},
		"temperature": 0.3,
	}
	if c.MaxTokens > 0 {
		payload["max_tokens"] = c.MaxTokens
	}
	body, err := c.postJSON(ctx, "/chat/completions", payload)
	if err != nil {
		return "", err
	}
	var out struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
		Error *struct{ Message string } `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("vision: decode: %w", err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return "", fmt.Errorf("vision: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("vision: 模型没有返回内容（可能不支持图片输入）")
	}
	return out.Choices[0].Message.Content, nil
}

// Chat 调用 /chat/completions，返回首个候选文本。
func (c *Client) Chat(ctx context.Context, system, user string) (string, error) {
	payload := map[string]any{
		"model": c.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"temperature": 0.7,
	}
	if c.MaxTokens > 0 {
		payload["max_tokens"] = c.MaxTokens
	}
	body, err := c.postJSON(ctx, "/chat/completions", payload)
	if err != nil {
		return "", err
	}
	var out struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("chat: decode: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("chat: empty choices")
	}
	return out.Choices[0].Message.Content, nil
}

// Transcribe 调用 /audio/transcriptions（multipart，16k 单声道 WAV 最稳）。
func (c *Client) Transcribe(ctx context.Context, wav []byte) (string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("model", c.Model); err != nil {
		return "", err
	}
	fw, err := mw.CreateFormFile("file", "utterance.wav")
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(wav); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/audio/transcriptions", &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("asr: http %d: %s", resp.StatusCode, truncate(raw))
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("asr: decode: %w", err)
	}
	return out.Text, nil
}

// Speak 调用 /audio/speech，返回音频字节（默认 mp3）。
func (c *Client) Speak(ctx context.Context, text, voice string) ([]byte, error) {
	payload := map[string]any{
		"model":           c.Model,
		"input":           text,
		"voice":           voice,
		"response_format": "wav",
	}
	body, err := c.postJSON(ctx, "/audio/speech", payload)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func (c *Client) postJSON(ctx context.Context, path string, payload any) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s: http %d: %s", path, resp.StatusCode, truncate(body))
	}
	return body, nil
}

func truncate(b []byte) string {
	if len(b) > 300 {
		return string(b[:300]) + "..."
	}
	return string(b)
}
