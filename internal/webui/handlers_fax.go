package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sipvoice/internal/aibuild"
	"sipvoice/internal/config"
	"sipvoice/internal/fax"
)

// handleFax 传真配置、接收列表、发送、预览。
func (s *Server) handleFax(w http.ResponseWriter, r *http.Request) {
	cfg := fax.Load()

	switch r.Method {
	case http.MethodGet:
		switch r.URL.Query().Get("action") {
		case "preview":
			files, err := fax.Preview(r.URL.Query().Get("file"), cfg)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			names := make([]string, 0, len(files))
			for _, f := range files {
				names = append(names, filepath.Base(f))
			}
			writeJSON(w, map[string]any{"ok": true, "pages": names})
			return
		case "image":
			p, err := fax.SafePreviewPath(r.URL.Query().Get("name"))
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			http.ServeFile(w, r, p)
			return
		case "download":
			name := filepath.Base(r.URL.Query().Get("file"))
			p := filepath.Join(cfg.Spool, name)
			if _, err := os.Stat(p); err != nil {
				http.Error(w, "文件不存在", 404)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
			http.ServeFile(w, r, p)
			return
		case "prompt":
			// 试听/下载已生成的开场提示音
			if !fax.PromptExists(cfg) {
				http.Error(w, "提示音还没生成", 404)
				return
			}
			w.Header().Set("Content-Type", "audio/wav")
			http.ServeFile(w, r, cfg.PromptFile)
			return
		case "status":
			writeJSON(w, map[string]any{"ok": true, "send": fax.Status(), "last": fax.ReadFaxResult()})
			return
		case "failed":
			name := filepath.Base(r.URL.Query().Get("file"))
			dir := cfg.FailDir
			if dir == "" {
				dir = fax.DefaultConfig().FailDir
			}
			p := filepath.Join(dir, name)
			if _, err := os.Stat(p); err != nil {
				http.Error(w, "文件不存在", 404)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
			http.ServeFile(w, r, p)
			return
		}
		items, err := fax.List(cfg)
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error(), "config": cfg})
			return
		}
		writeJSON(w, map[string]any{
			"ok":           true,
			"config":       cfg,
			"items":        items,
			"failed":       fax.ListFailed(cfg),
			"send":         fax.Status(),
			"last":         fax.ReadFaxResult(),
			"prompt_ready": fax.PromptExists(cfg),
		})

	case http.MethodPost:
		var in struct {
			Action string     `json:"action"`
			Config fax.Config `json:"config"`
			File   string     `json:"file"`
			Dest   string     `json:"dest"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		switch in.Action {
		case "save":
			c := in.Config
			c.Normalize()
			if err := fax.Save(c); err != nil {
				writeJSON(w, map[string]any{"ok": false, "message": err.Error()})
				return
			}
			if err := fax.ApplyDialplan(c); err != nil {
				writeJSON(w, map[string]any{"ok": false, "message": "配置已保存，但拨号方案应用失败: " + err.Error()})
				return
			}
			writeJSON(w, map[string]any{"ok": true, "message": "传真配置已保存并生效"})
		case "send":
			if in.Config.PromptEnabled && !fax.PromptExists(in.Config) {
				writeJSON(w, map[string]any{"ok": false, "message": "提示音还没生成，请先在设置里点「生成提示音」"})
				return
			}
			go fax.Send(cfg, in.File, in.Dest)
			writeJSON(w, map[string]any{"ok": true, "message": "已开始发送"})
		case "genprompt":
			// 用当前 TTS 服务商合成一次开场提示音（计费一次，之后一直复用）
			text := strings.TrimSpace(in.Config.PromptText)
			if text == "" {
				text = fax.DefaultConfig().PromptText
			}
			tts := aibuild.BuildTTS(config.Get(), "")
			if tts == nil {
				writeJSON(w, map[string]any{"ok": false, "message": "TTS 未配置（AI 服务页先填好 TTS）"})
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
			defer cancel()
			audio, err := tts.Speak(ctx, text)
			if err != nil {
				writeJSON(w, map[string]any{"ok": false, "message": "TTS 合成失败: " + err.Error()})
				return
			}
			c := in.Config
			c.Normalize()
			if _, err := fax.SavePromptWAV(c, audio); err != nil {
				writeJSON(w, map[string]any{"ok": false, "message": err.Error()})
				return
			}
			if err := fax.Save(c); err != nil {
				writeJSON(w, map[string]any{"ok": false, "message": err.Error()})
				return
			}
			if err := fax.ApplyDialplan(c); err != nil {
				writeJSON(w, map[string]any{"ok": false, "message": "提示音已生成，但拨号方案应用失败: " + err.Error()})
				return
			}
			writeJSON(w, map[string]any{"ok": true, "message": "提示音已生成，可以点「试听」确认"})
		default:
			http.Error(w, "未知操作", 400)
		}
	default:
		http.Error(w, "method not allowed", 405)
	}
}
