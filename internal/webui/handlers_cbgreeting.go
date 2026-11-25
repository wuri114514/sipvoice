package webui

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"sipvoice/internal/aibuild"
	"sipvoice/internal/config"
)

const callbackIntroFile = "/var/lib/sipvoice/prompts/callback-intro.wav"

// handleCallbackIntro 生成/试听 AI 回拨的开场白。
//
//	POST {text:"..."} → TTS 合成并写成 8k wav，同时写入 AstDB
//	GET               → 返回音频
func (s *Server) handleCallbackIntro(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if _, err := os.Stat(callbackIntroFile); err != nil {
			http.Error(w, "开场白还没生成", 404)
			return
		}
		w.Header().Set("Content-Type", "audio/wav")
		http.ServeFile(w, r, callbackIntroFile)
	case http.MethodPost:
		var in struct {
			Text string `json:"text"`
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		text := strings.TrimSpace(in.Text)
		if text == "" {
			text = config.Get().Session.CallbackGreeting
		}
		if text == "" {
			text = "传真已被接收并分析，让我们继续。"
		}
		tts := aibuild.BuildTTS(config.Get(), "")
		if tts == nil {
			writeJSON(w, map[string]any{"ok": false, "message": "TTS 未配置"})
			return
		}
		audio, err := tts.Speak(r.Context(), text)
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "message": "合成失败: " + err.Error()})
			return
		}
		// 转 8k 单声道
		tmp, _ := os.CreateTemp("", "cbin-*")
		tmp.Write(audio)
		tmp.Close()
		defer os.Remove(tmp.Name())
		if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
			"-i", tmp.Name(), "-ar", "8000", "-ac", "1", "-c:a", "pcm_s16le", callbackIntroFile,
		).CombinedOutput(); err != nil {
			writeJSON(w, map[string]any{"ok": false, "message": "转码失败: " + strings.TrimSpace(string(out))})
			return
		}
		_ = exec.Command("chown", "sipvoice:sipvoice", callbackIntroFile).Run()
		// 写入 AstDB，供拨号方案读取
		for _, prog := range [][]string{
			{"sudo", "-n", "/usr/sbin/asterisk", "-rx",
				"database put sipvoice callback_intro /var/lib/sipvoice/prompts/callback-intro"},
			{"/usr/sbin/asterisk", "-rx",
				"database put sipvoice callback_intro /var/lib/sipvoice/prompts/callback-intro"},
		} {
			_ = exec.Command(prog[0], prog[1:]...).Run()
		}
		writeJSON(w, map[string]any{"ok": true, "message": "开场白已生成"})
	default:
		http.Error(w, "method not allowed", 405)
	}
}
