package worker

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"sipvoice/internal/aibuild"
	"sipvoice/internal/fax"

	"sipvoice/internal/config"
	"sipvoice/internal/session"
)

// NotifyFaxArrived 传真分析完成后调用：按配置的延迟与策略决定是否自动回拨，
// 让对方知道“传真我看过了”。
func (w *Worker) NotifyFaxArrived(ctx context.Context, key, summary string) {
	if w.Sessions == nil || key == "" {
		return
	}
	c := config.Get()
	delay := time.Duration(c.Session.CallbackDelaySec) * time.Second
	if delay < 0 {
		delay = 0
	}

	// 不管是否自动回拨，都先把分析结果写进会话（前面可能因为文件名没号码漏掉了）
	w.Sessions.Ensure(key)
	w.Sessions.Append(key, session.RoleAssistant,
		"（系统提示：你刚收到一页传真，内容是——"+summary+"）", "fax")
	w.Sessions.EndCall(key)

	if !c.Session.AutoCallbackOnFax || c.Session.CallbackPolicy == "never" {
		log.Printf("[worker] 传真已分析，自动回拨未启用（号码 %s）", key)
		return
	}

	// 把传真摘要合成音频，回拨时播给用户听（TTS 只在这里调一次）
	if err := synthesizeFaxSummary(summary); err != nil {
		log.Printf("[worker] 传真摘要合成音频失败（回拨将只播开场白）: %v", err)
	}

	log.Printf("[worker] 传真分析完成，%s 后回拨 %s", delay, key)
	// 回拨流程：开场白 → 传真摘要 → 「请在滴声后继续通话」→ 滴 → 接进 AI 对话
	w.Sessions.AddTask(key, session.Task{
		Type: "callback",
		Params: map[string]any{
			"after_fax": true,
			"delay":     int(delay.Seconds()),
		},
	})
}

// 让 tasks 支持延迟执行：worker 里按 CreatedAt + delay 判断是否到点
func taskReady(at time.Time, params map[string]any) bool {
	if params == nil {
		return true
	}
	d, ok := params["delay"]
	if !ok {
		return true
	}
	var sec int
	switch v := d.(type) {
	case int:
		sec = v
	case float64:
		sec = int(v)
	case int64:
		sec = int(v)
	}
	if sec <= 0 {
		return true
	}
	return time.Since(at) >= time.Duration(sec)*time.Second
}

var _ = fmt.Sprint

// synthesizeFaxSummary 把传真摘要用 TTS 合成成 8k wav，路径写入 AstDB，
// 供 fax-callback 在回拨时播放。
func synthesizeFaxSummary(summary string) error {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return fmt.Errorf("摘要为空")
	}
	// 太长就截断（回拨不适合念大段文字）
	if r := []rune(summary); len(r) > 300 {
		summary = string(r[:300]) + "。"
	}
	cfg := config.Get()
	tts := aibuild.BuildTTS(cfg, "")
	if tts == nil {
		return fmt.Errorf("TTS 未配置")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	audio, err := tts.Speak(ctx, summary)
	if err != nil {
		return err
	}
	out := "/var/lib/sipvoice/prompts/callback-summary.wav"
	if _, err := fax.SavePromptWAV(fax.Config{PromptFile: out}, audio); err != nil {
		return err
	}
	_ = exec.Command("chown", "sipvoice:sipvoice", out).Run()
	for _, prog := range [][]string{
		{"sudo", "-n", "/usr/sbin/asterisk", "-rx", "database put sipvoice callback_summary /var/lib/sipvoice/prompts/callback-summary"},
		{"/usr/sbin/asterisk", "-rx", "database put sipvoice callback_summary /var/lib/sipvoice/prompts/callback-summary"},
	} {
		_ = exec.Command(prog[0], prog[1:]...).Run()
	}
	return nil
}
