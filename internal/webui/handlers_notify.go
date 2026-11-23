package webui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"sipvoice/internal/config"
	"sipvoice/internal/notify"
	"sipvoice/internal/omni"
)

// handleTestOmni 用当前（或临时覆盖的）配置开一条实时会话，说一句话验证连通性。
func (s *Server) handleTestOmni(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Model string `json:"model"`
		Voice string `json:"voice"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in)
	}
	cfg := config.Get()
	rc := cfg.Realtime
	if v := strings.TrimSpace(in.Model); v != "" {
		rc.Model = v
	}
	if v := strings.TrimSpace(in.Voice); v != "" {
		rc.Voice = v
	}
	if rc.APIKey == "" {
		writeJSON(w, map[string]any{"ok": false, "error": "未配置实时语音 API Key"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	c := &omni.Client{Cfg: omni.Config{
		BaseURL:      rc.BaseURL,
		APIKey:       rc.APIKey,
		Model:        rc.Model,
		Voice:        rc.Voice,
		Instructions: rc.Instructions,
	}}
	t0 := time.Now()
	sess, err := c.Open(ctx)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	defer sess.Close()
	openMS := time.Since(t0).Milliseconds()

	turn, err := sess.Say(ctx, "请说一句：语音连接测试成功。")
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error(), "open_ms": openMS})
		return
	}
	total := 0
	for chunk := range drainAudio(ctx, turn.Audio) {
		total += len(chunk)
	}
	_, reply, terr := turn.Wait()
	if terr != nil && terr != context.Canceled {
		writeJSON(w, map[string]any{"ok": false, "error": terr.Error(), "open_ms": openMS, "reply": reply})
		return
	}
	firstMS := int64(-1)
	if first := turn.FirstAudioAt(); !first.IsZero() {
		firstMS = first.Sub(t0).Milliseconds()
	}
	writeJSON(w, map[string]any{
		"ok":        true,
		"model":     rc.Model,
		"voice":     rc.Voice,
		"open_ms":   openMS,
		"first_ms":  firstMS,
		"audio_sec": float64(total) / 8000,
		"reply":     reply,
	})
}

// drainAudio 把回合音频排干（测试用，不播放），遇到结束标记或 ctx 取消即返回。
func drainAudio(ctx context.Context, ch <-chan []int16) <-chan []int16 {
	out := make(chan []int16)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case pcm, ok := <-ch:
				if !ok || pcm == nil {
					return
				}
				out <- pcm
			}
		}
	}()
	return out
}

// handleTestNotify 发送一条 ServerChan 测试推送。
func (s *Server) handleTestNotify(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Key string `json:"key"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in)
	}
	cfg := config.Get()
	key := strings.TrimSpace(in.Key)
	if key == "" {
		key = cfg.Notify.ServerChanKey
	}
	if key == "" {
		writeJSON(w, map[string]any{"ok": false, "error": "未配置 SendKey"})
		return
	}
	n := notify.New(key, time.Minute)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	err := n.SendOnce(ctx, "SIPVoice 测试推送", "看到这条消息说明 ServerChan 已经通了。\n\n时间："+time.Now().Format("2006-01-02 15:04:05"))
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
