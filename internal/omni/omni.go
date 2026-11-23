// Package omni 封装 Qwen-Omni-Realtime 这类“端到端语音”模型的会话。
//
// 与 ASR→LLM→TTS 三段式不同，这类模型直接吃 16k PCM、吐 24k PCM，
// 一轮对话只有一次往返：说完 → 提交 → 边生成边回传音频，首包通常在 0.5s 左右。
//
// 会话内只有一条常驻读协程，事件按“当前回合”分发，避免多轮之间抢同一个 socket。
// VAD 断句、打断、DTMF 仍由 voice.Agent 负责（Manual 模式）。
package omni

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"sipvoice/internal/media"
	"sipvoice/internal/ws"
)

const (
	// InputRate 是模型要求的输入采样率。
	InputRate = 16000
	// OutputRate 是模型回传音频的采样率。
	OutputRate = 24000
	// 每次 append 送 100ms 音频，兼顾实时性与包数。
	chunkSamples = InputRate / 10
	// 会话空闲多久算超时（期间没有任何事件）。
	idleTimeout = 5 * time.Minute
)

// Config 描述一条 realtime 连接。
type Config struct {
	BaseURL      string // 例如 wss://dashscope.aliyuncs.com/api-ws/v1/realtime
	APIKey       string
	Model        string
	Voice        string
	Instructions string
}

// Client 是构造会话的工厂。
type Client struct {
	Cfg Config
}

// Session 是一条已建立的实时会话（一通电话一条）。
type Session struct {
	cli *ws.Client
	cfg Config

	mu  sync.Mutex
	cur *Turn
}

// Turn 是一次问答，音频边到边播。
type Turn struct {
	Audio <-chan []int16 // 已重采样到 8k
	ch    chan []int16

	mu         sync.Mutex
	closed     bool
	transcript string
	reply      string
	err        error
	firstAt    time.Time
	acc24      []int16

	once sync.Once
	done chan struct{}
}

// Wait 阻塞到本轮结束，返回（用户说了什么，助手说了什么，错误）。
func (t *Turn) Wait() (string, string, error) {
	<-t.done
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.transcript, t.reply, t.err
}

// FirstAudioAt 返回第一块音频到达的时间（零值表示没收到音频）。
func (t *Turn) FirstAudioAt() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.firstAt
}

func newTurn() *Turn {
	ch := make(chan []int16, 64)
	return &Turn{Audio: ch, ch: ch, done: make(chan struct{})}
}

func (t *Turn) setUser(s string) {
	t.mu.Lock()
	t.transcript = s
	t.mu.Unlock()
}

func (t *Turn) addReply(s string) {
	t.mu.Lock()
	t.reply += s
	t.mu.Unlock()
}

// finish 标记回合结束并投递一个 nil 作为结束信号（只生效一次）。
func (t *Turn) finish(err error) {
	t.once.Do(func() {
		t.mu.Lock()
		if err != nil && t.err == nil {
			t.err = err
		}
		t.closed = true
		t.mu.Unlock()
		close(t.done)
		select {
		case t.ch <- nil:
		case <-time.After(time.Second):
		}
	})
}

// pushAudio 把重采样后的 8k 音频交给播放端；消费者不要了就丢弃。
func (t *Turn) pushAudio(pcm []int16) {
	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	if closed {
		return
	}
	select {
	case t.ch <- pcm:
	case <-time.After(5 * time.Second):
	case <-t.done:
	}
}

// Open 建立会话并完成 session.update。
func (c *Client) Open(ctx context.Context) (*Session, error) {
	base := strings.TrimRight(c.Cfg.BaseURL, "/")
	if base == "" {
		base = "wss://dashscope.aliyuncs.com/api-ws/v1/realtime"
	}
	if c.Cfg.APIKey == "" {
		return nil, fmt.Errorf("omni: 未配置 API Key")
	}
	u := base + "?model=" + c.Cfg.Model
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+c.Cfg.APIKey)
	hdr.Set("User-Agent", "sipvoice/1.2")

	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cli, err := ws.Dial(dctx, u, hdr)
	if err != nil {
		return nil, fmt.Errorf("omni: 连接失败: %w", err)
	}

	s := &Session{cli: cli, cfg: c.Cfg}
	if _, err := s.waitEvent(dctx, "session.created"); err != nil {
		cli.Close()
		return nil, err
	}
	sess := map[string]any{
		"modalities":     []string{"text", "audio"},
		"voice":          c.Cfg.Voice,
		"instructions":   c.Cfg.Instructions,
		"turn_detection": nil, // Manual：由我们自己的 VAD 断句
	}
	if err := s.send(map[string]any{"type": "session.update", "session": sess}); err != nil {
		cli.Close()
		return nil, err
	}
	if _, err := s.waitEvent(dctx, "session.updated"); err != nil {
		cli.Close()
		return nil, err
	}
	go s.readLoop()
	return s, nil
}

// Close 关闭会话。
func (s *Session) Close() error {
	s.mu.Lock()
	t := s.cur
	s.cur = nil
	s.mu.Unlock()
	if t != nil {
		t.finish(fmt.Errorf("会话关闭"))
	}
	return s.cli.Close()
}

// Ask 把一段 8k 单声道语音发给模型，返回流式回合。
func (s *Session) Ask(ctx context.Context, pcm8 []int16) (*Turn, error) {
	t, err := s.beginTurn()
	if err != nil {
		return nil, err
	}
	pcm16 := media.Resample(pcm8, 8000, InputRate)

	if err := s.send(map[string]any{"type": "input_audio_buffer.clear"}); err != nil {
		s.endTurn(t, err)
		return nil, err
	}
	for off := 0; off < len(pcm16); off += chunkSamples {
		end := off + chunkSamples
		if end > len(pcm16) {
			end = len(pcm16)
		}
		chunk := make([]byte, (end-off)*2)
		for i, v := range pcm16[off:end] {
			binary.LittleEndian.PutUint16(chunk[i*2:], uint16(v))
		}
		if err := s.send(map[string]any{
			"type":  "input_audio_buffer.append",
			"audio": base64.StdEncoding.EncodeToString(chunk),
		}); err != nil {
			s.endTurn(t, err)
			return nil, err
		}
	}
	if err := s.send(map[string]any{"type": "input_audio_buffer.commit"}); err != nil {
		s.endTurn(t, err)
		return nil, err
	}
	if err := s.send(map[string]any{"type": "response.create"}); err != nil {
		s.endTurn(t, err)
		return nil, err
	}
	return t, nil
}

// Say 让模型开口说一段（用于开场白）。
func (s *Session) Say(ctx context.Context, hint string) (*Turn, error) {
	t, err := s.beginTurn()
	if err != nil {
		return nil, err
	}
	item := map[string]any{
		"type": "conversation.item.create",
		"item": map[string]any{
			"type":    "message",
			"role":    "user",
			"content": []map[string]any{{"type": "input_text", "text": hint}},
		},
	}
	if err := s.send(item); err != nil {
		s.endTurn(t, err)
		return nil, err
	}
	if err := s.send(map[string]any{"type": "response.create"}); err != nil {
		s.endTurn(t, err)
		return nil, err
	}
	return t, nil
}

// Cancel 让服务端停止当前回复，并立即结束本地回合。
func (s *Session) Cancel() {
	s.mu.Lock()
	t := s.cur
	s.cur = nil
	s.mu.Unlock()
	if t == nil {
		return
	}
	_ = s.send(map[string]any{"type": "response.cancel"})
	t.finish(nil)
}

func (s *Session) beginTurn() (*Turn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur != nil {
		return nil, fmt.Errorf("omni: 上一轮尚未结束")
	}
	t := newTurn()
	s.cur = t
	return t, nil
}

func (s *Session) endTurn(t *Turn, err error) {
	s.mu.Lock()
	if s.cur == t {
		s.cur = nil
	}
	s.mu.Unlock()
	t.finish(err)
}

func (s *Session) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.cli.WriteText(b)
}

func (s *Session) waitEvent(ctx context.Context, want string) (json.RawMessage, error) {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		op, data, err := s.cli.ReadMessage(rctx)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("omni: 等待 %s 失败: %w", want, err)
		}
		if op != 1 {
			continue
		}
		var head struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(data, &head)
		if head.Type == want {
			return data, nil
		}
		if head.Type == "error" {
			return nil, fmt.Errorf("omni: %s", string(data))
		}
	}
	return nil, fmt.Errorf("omni: 等待 %s 超时", want)
}

// readLoop 是会话内唯一的读协程，按类型把事件分发给当前回合。
func (s *Session) readLoop() {
	for {
		rctx, cancel := context.WithTimeout(context.Background(), idleTimeout)
		op, data, err := s.cli.ReadMessage(rctx)
		cancel()
		if err != nil {
			s.mu.Lock()
			t := s.cur
			s.cur = nil
			s.mu.Unlock()
			if t != nil {
				t.finish(fmt.Errorf("omni: 连接中断: %w", err))
			}
			return
		}
		if op != 1 {
			continue
		}
		s.dispatch(data)
	}
}

func (s *Session) dispatch(data []byte) {
	var ev struct {
		Type       string          `json:"type"`
		Delta      string          `json:"delta"`
		Transcript string          `json:"transcript"`
		Error      json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(data, &ev); err != nil {
		return
	}
	if ev.Type == "session.updated" || ev.Type == "response.created" {
		return
	}

	s.mu.Lock()
	t := s.cur
	s.mu.Unlock()
	if t == nil {
		return
	}

	switch ev.Type {
	case "response.audio.delta":
		raw, err := base64.StdEncoding.DecodeString(ev.Delta)
		if err != nil {
			return
		}
		t.mu.Lock()
		for i := 0; i+1 < len(raw); i += 2 {
			t.acc24 = append(t.acc24, int16(binary.LittleEndian.Uint16(raw[i:])))
		}
		ready := len(t.acc24) >= OutputRate/5 // 每 200ms 播一次
		var block []int16
		if t.firstAt.IsZero() {
			t.firstAt = time.Now()
		}
		if ready {
			block = media.Resample(t.acc24, OutputRate, 8000)
			t.acc24 = nil
		}
		t.mu.Unlock()
		if ready && len(block) > 0 {
			t.pushAudio(block)
		}
	case "response.audio_transcript.delta", "response.text.delta":
		if ev.Delta != "" {
			t.addReply(ev.Delta)
		}
	case "conversation.item.input_audio_transcription.completed":
		t.setUser(ev.Transcript)
	case "response.done":
		t.mu.Lock()
		if len(t.acc24) > 0 {
			block := media.Resample(t.acc24, OutputRate, 8000)
			t.acc24 = nil
			t.mu.Unlock()
			if len(block) > 0 {
				t.pushAudio(block)
			}
		} else {
			t.mu.Unlock()
		}
		s.endTurn(t, nil)
	case "error":
		// 取消已结束的回复时，服务端会回 "Conversation has none active response"，忽略即可
		if strings.Contains(string(data), "none active response") {
			return
		}
		s.endTurn(t, fmt.Errorf("omni: %s", string(data)))
	}
}

// PCM16 把小端字节转成 int16（供测试用）。
func PCM16(b []byte) []int16 {
	out := make([]int16, len(b)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[i*2:]))
	}
	return out
}

// Bytes 把 int16 转回小端字节。
func Bytes(pcm []int16) []byte {
	buf := new(bytes.Buffer)
	_ = binary.Write(buf, binary.LittleEndian, pcm)
	return buf.Bytes()
}
