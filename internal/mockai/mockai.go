// Package mockai 提供一个本地假的 OpenAI 兼容服务，用于在拿到真实 Key 前
// 联调语音链路：ASR 返回固定文本，Chat 返回固定回复，TTS 返回提示音 WAV。
package mockai

import (
	"encoding/json"
	"log"
	"math"
	"net/http"
	"time"

	"sipvoice/internal/media"
)

// Start 在 addr 上启动 mock 服务，阻塞直到出错。
func Start(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/audio/transcriptions", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[mockai] ASR 请求 %s", r.Header.Get("Content-Type"))
		writeJSON(w, map[string]any{"text": "你好，我想问一下今天天气怎么样"})
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		user := ""
		for _, m := range req.Messages {
			if m.Role == "user" {
				user = m.Content
			}
		}
		log.Printf("[mockai] Chat 请求: %q", user)
		writeJSON(w, map[string]any{
			"choices": []map[string]any{{
				"message": map[string]string{"role": "assistant", "content": "今天晴，气温二十五度，适合出门。"},
			}},
		})
	})
	mux.HandleFunc("/v1/audio/speech", func(w http.ResponseWriter, r *http.Request) {
		pcm := beep(24000, 2*time.Second)
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(media.EncodeWAV(pcm, 24000))
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": []map[string]string{{"id": "mock"}}})
	})
	log.Printf("[mockai] 监听 %s", addr)
	return http.ListenAndServe(addr, mux)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// beep 生成一段三音提示音，便于人工听出"AI 在说话"。
func beep(rate int, dur time.Duration) []int16 {
	n := int(dur.Seconds() * float64(rate))
	out := make([]int16, n)
	freqs := []float64{440, 554, 659}
	for i := 0; i < n; i++ {
		t := float64(i) / float64(rate)
		// 每 0.66s 换一个音
		idx := int(t/0.66) % len(freqs)
		// 每段结尾做 20ms 淡出，避免爆音
		segT := math.Mod(t, 0.66)
		env := 1.0
		if segT > 0.62 {
			env = (0.66 - segT) / 0.04
		}
		out[i] = int16(0.25 * 32767 * env * math.Sin(2*math.Pi*freqs[idx]*t))
	}
	return out
}
