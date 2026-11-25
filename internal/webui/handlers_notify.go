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

// handleTestNotify 发送测试推送：ServerChan 与邮局两条通道都试一遍。
func (s *Server) handleTestNotify(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Key   string `json:"key"`
		Mail  bool   `json:"mail"`
		Title string `json:"title"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in)
	}
	cfg := config.Get()
	key := strings.TrimSpace(in.Key)
	if key == "" {
		key = cfg.Notify.ServerChanKey
	}

	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()

	title := "SIPVoice 测试推送"
	body := "看到这条消息说明通知通道已经通了。\n\n时间：" + time.Now().Format("2006-01-02 15:04:05")
	if strings.TrimSpace(in.Title) != "" {
		title = strings.TrimSpace(in.Title)
	}

	var (
		okList  []string
		errList []string
	)

	// 通道 1：ServerChan
	if key != "" {
		n := notify.New(key, time.Minute)
		if err := n.SendOnce(ctx, title, body); err != nil {
			errList = append(errList, "ServerChan: "+err.Error())
		} else {
			okList = append(okList, "ServerChan")
		}
	}

	// 通道 2：邮局（面板勾选时才测）
	if in.Mail || cfg.Notify.MailEnabled {
		mc := notify.NewMailChannel("/etc/sipvoice/mail.json", time.Minute)
		if cfg.Notify.MailSubject != "" {
			mc.Subject = cfg.Notify.MailSubject
		}
		if cfg.Notify.MailBody != "" {
			mc.Body = cfg.Notify.MailBody
		}
		if err := mc.SendOnce(ctx, title, body); err != nil {
			errList = append(errList, "邮局: "+err.Error())
		} else {
			okList = append(okList, "邮局")
		}
	}

	res := map[string]any{"ok": len(okList) > 0, "channels": okList}
	if len(errList) > 0 {
		res["errors"] = errList
	}
	if len(okList) == 0 && len(errList) == 0 {
		res["error"] = "没有可用的通知通道（SendKey 与邮局都没配）"
	}
	writeJSON(w, res)
}
