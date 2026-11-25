package webui

import (
	"encoding/json"
	"net/http"

	"sipvoice/internal/session"
)

// handleSessionTask 给指定会话挂一个异步任务（面板调试 / AI 识别后调用）。
//
//	POST /api/session/task {key:"1001", type:"fax", params:{dest,file}}
//	POST /api/session/task {key:"1001", type:"callback", params:{message}}
func (s *Server) handleSessionTask(w http.ResponseWriter, r *http.Request) {
	if s.opt.Sessions == nil {
		writeJSON(w, map[string]any{"ok": false, "error": "会话管理器未初始化"})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var in struct {
		Key    string         `json:"key"`
		Type   string         `json:"type"`
		Params map[string]any `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if in.Key == "" || in.Type == "" {
		writeJSON(w, map[string]any{"ok": false, "error": "缺少 key 或 type"})
		return
	}
	// 确保会话存在（面板调试时可能还没打过电话）
	s.opt.Sessions.Ensure(in.Key)
	s.opt.Sessions.AddTask(in.Key, session.Task{Type: in.Type, Params: in.Params})
	writeJSON(w, map[string]any{"ok": true, "message": "任务已加入会话 " + in.Key})
}
