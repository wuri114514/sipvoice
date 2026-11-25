package webui

import (
	"encoding/json"
	"net/http"

	"sipvoice/internal/session"
)

// handleSessions 会话查看与操作。
//
//	GET  /api/sessions            → 活跃会话列表
//	POST /api/sessions {action:"clear", key:"1001"} → 清空某会话
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if s.opt.Sessions == nil {
		writeJSON(w, map[string]any{"ok": false, "error": "会话管理器未初始化", "sessions": []any{}})
		return
	}
	switch r.Method {
	case http.MethodGet:
		// ?key=1003 时返回该会话的完整消息
		if key := r.URL.Query().Get("key"); key != "" {
			msgs := s.opt.Sessions.History(key)
			out := make([]map[string]any, 0, len(msgs))
			for _, m := range msgs {
				out = append(out, map[string]any{
					"role":    m.Role,
					"content": m.Content,
					"kind":    m.Kind,
					"at":      m.At.Format("15:04:05"),
				})
			}
			writeJSON(w, map[string]any{"ok": true, "key": key, "messages": out})
			return
		}
		list := s.opt.Sessions.List()
		if list == nil {
			list = []map[string]any{}
		}
		writeJSON(w, map[string]any{"ok": true, "sessions": list})
	case http.MethodPost:
		var in struct {
			Action string `json:"action"`
			Key    string `json:"key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		switch in.Action {
		case "clear":
			if in.Key == "" {
				http.Error(w, "缺少 key", 400)
				return
			}
			s.opt.Sessions.Clear(in.Key)
			writeJSON(w, map[string]any{"ok": true, "message": "已清空会话 " + in.Key})
		case "clearall":
			for _, item := range s.opt.Sessions.List() {
				if k, ok := item["key"].(string); ok {
					s.opt.Sessions.Clear(k)
				}
			}
			writeJSON(w, map[string]any{"ok": true, "message": "已清空全部会话"})
		default:
			http.Error(w, "未知操作", 400)
		}
	default:
		http.Error(w, "method not allowed", 405)
	}
}

// 防止未使用的导入（session 仅在类型断言时需要，这里显式引用一次）
var _ = session.StateIdle
