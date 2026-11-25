package webui

import (
	"context"
	"net/http"
	"time"

	"sipvoice/internal/fax"
)

// handleFaxOCR 用视觉模型分析一份已收到的传真，并把结果缓存成同名 .txt。
//
//	POST /api/fax/analyze {file:"20261125-...tiff", save_session:"1001"}
func (s *Server) handleFaxAnalyze(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var in struct {
		File   string `json:"file"`
		SaveTo string `json:"save_session"` // 可选：把结果写进某个会话
		Prompt string `json:"prompt"`
	}
	if err := decodeJSON(r, &in); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	cfg := fax.Load()
	pages, err := fax.Preview(in.File, cfg)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if len(pages) == 0 {
		writeJSON(w, map[string]any{"ok": false, "error": "没有可分析的页面"})
		return
	}
	prompt := in.Prompt
	if prompt == "" {
		prompt = "这是一页传真。请用简洁的中文说明它的内容要点；如果包含金额、日期、人名、编号等重要信息，请一并列出。"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 180*time.Second)
	defer cancel()
	text, err := analyzeImageFiles(ctx, pages, prompt)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	_ = fax.SaveText(in.File, text)
	// 可选：写进会话上下文
	if in.SaveTo != "" && s.opt.Sessions != nil {
		s.opt.Sessions.Ensure(in.SaveTo)
		s.opt.Sessions.Append(in.SaveTo, "assistant",
			"（系统提示：你刚收到一页传真，内容是——"+text+"）", "fax")
	}
	_ = ctx
	writeJSON(w, map[string]any{"ok": true, "text": text, "pages": len(pages)})
}
