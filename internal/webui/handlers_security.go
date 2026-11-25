package webui

import (
	"encoding/json"
	"net/http"
)

// handleSecurity 返回登录防护状态，并支持手动解封。
func (s *Server) handleSecurity(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{
			"blocked":    s.guard.status(),
			"max_fails":  loginMaxFails,
			"window_sec": int(loginWindow.Seconds()),
			"block_sec":  int(loginBlockDur.Seconds()),
		})
	case http.MethodPost:
		var in struct {
			IP string `json:"ip"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if in.IP == "" {
			http.Error(w, "缺少 ip", 400)
			return
		}
		s.guard.unblock(in.IP)
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", 405)
	}
}
