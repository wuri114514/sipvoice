package webui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"sipvoice/internal/aibuild"
	"sipvoice/internal/adapters"
	"sipvoice/internal/config"
	"sipvoice/internal/media"
	"sipvoice/internal/providers"
)

// handleProviders 返回内置服务商预设，供面板渲染下拉框。
func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	writeJSON(w, map[string]any{
		"presets": providers.All(),
		"current": cfg.AI.Provider,
		"guessed": providers.Guess(cfg.AI.BaseURL),
	})
}

// handleRemoteModels 在线拉取模型列表：GET /api/models/remote?kind=chat&base=&protocol=&field=
// key 传空或 *** 时使用已保存的对应 Key。
func (s *Server) handleRemoteModels(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind := q.Get("kind")
	base := strings.TrimSpace(q.Get("base"))
	protocol := q.Get("protocol")
	key := strings.TrimSpace(q.Get("key"))

	cfg := config.Get()
	if base == "" {
		base = cfg.AI.BaseURL
	}
	if key == "" || key == "***" {
		switch q.Get("field") {
		case "asr":
			key = firstNonEmpty(cfg.AI.ASRKey, cfg.AI.APIKey)
		case "tts":
			key = firstNonEmpty(cfg.AI.TTSKey, cfg.AI.APIKey)
		default:
			key = firstNonEmpty(cfg.AI.APIKey, cfg.AI.ASRKey)
		}
	}
	if protocol == "" {
		if p, ok := providers.Get(q.Get("provider")); ok && p.Protocol != "" {
			protocol = p.Protocol
		}
	}

	start := time.Now()
	all, err := adapters.ListModels(r.Context(), protocol, base, key, true)
	ms := time.Since(start).Milliseconds()
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error(), "ms": ms})
		return
	}
	models := adapters.FilterByKind(kind, all)
	writeJSON(w, map[string]any{
		"ok":     true,
		"kind":   kind,
		"models": models,
		"all":    all,
		"total":  len(all),
		"ms":     ms,
	})
}

// handleTestTTS 在线合成一段试听音频，返回 base64 音频。
func (s *Server) handleTestTTS(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text  string `json:"text"`
		Voice string `json:"voice"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	in.Text = strings.TrimSpace(in.Text)
	if in.Text == "" {
		in.Text = "您好，这是一段语音合成测试。"
	}
	if n := len([]rune(in.Text)); n > 500 {
		in.Text = string([]rune(in.Text)[:500])
	}

	cfg := config.Get()
	tts := aibuild.BuildTTS(cfg, strings.TrimSpace(in.Voice))
	if tts == nil {
		writeJSON(w, map[string]any{"ok": false, "error": "TTS 未启用（后端为 off 或缺少 Base URL）"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	start := time.Now()
	audio, err := tts.Speak(ctx, in.Text)
	ms := time.Since(start).Milliseconds()
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error(), "ms": ms})
		return
	}
	if len(audio) == 0 {
		writeJSON(w, map[string]any{"ok": false, "error": "返回音频为空", "ms": ms})
		return
	}
	writeJSON(w, map[string]any{
		"ok":       true,
		"ms":       ms,
		"bytes":    len(audio),
		"mime":     audioMIME(audio),
		"audio":    base64.StdEncoding.EncodeToString(audio),
		"backend":  aibuild.TTSBackendName(cfg),
		"voice":    strings.TrimSpace(in.Voice),
		"duration": media.DurationSeconds(audio),
	})
}

// handleTestASR 在线识别上传的音频（任意格式，ffmpeg 统一转 16k 单声道）。
func (s *Server) handleTestASR(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "上传解析失败: " + err.Error()})
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "缺少音频文件（字段 file）"})
		return
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 64<<20))
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "读取文件失败: " + err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 180*time.Second)
	defer cancel()

	name := "upload"
	if hdr != nil {
		name = hdr.Filename
	}
	wav, err := media.ConvertToWAV16k(ctx, raw, name)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	dur := media.DurationSeconds(wav)

	cfg := config.Get()
	asr := aibuild.BuildASR(cfg)
	if asr == nil {
		writeJSON(w, map[string]any{"ok": false, "error": "ASR 未启用（后端为 off 或缺少配置）"})
		return
	}

	start := time.Now()
	text, err := asr.Transcribe(ctx, wav)
	ms := time.Since(start).Milliseconds()
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error(), "ms": ms})
		return
	}
	writeJSON(w, map[string]any{
		"ok":       true,
		"text":     strings.TrimSpace(text),
		"ms":       ms,
		"backend":  aibuild.ASRBackendName(cfg),
		"model":    cfg.AI.ASRModel,
		"duration": dur,
	})
}

func audioMIME(b []byte) string {
	if len(b) >= 4 && string(b[:4]) == "RIFF" {
		return "audio/wav"
	}
	if len(b) >= 3 && string(b[:3]) == "ID3" {
		return "audio/mpeg"
	}
	if len(b) >= 2 && b[0] == 0xFF && b[1]&0xE0 == 0xE0 {
		return "audio/mpeg"
	}
	if len(b) >= 4 && string(b[:4]) == "OggS" {
		return "audio/ogg"
	}
	return "audio/wav"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
