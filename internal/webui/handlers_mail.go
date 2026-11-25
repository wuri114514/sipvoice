package webui

import (
	"encoding/json"
	"net/http"
	"strings"

	"sipvoice/internal/mail"
)

// handleMail 读取/保存邮件设置，发送测试邮件。
func (s *Server) handleMail(w http.ResponseWriter, r *http.Request) {
	store := &mail.Store{Path: "/etc/sipvoice/mail.json"}

	switch r.Method {
	case http.MethodGet:
		st := store.Load()
		// 不回传授权码明文，只标记是否已设置
		st.Primary.Password = maskSecret(st.Primary.Password)
		st.Backup.Password = maskSecret(st.Backup.Password)
		writeJSON(w, map[string]any{"ok": true, "settings": st, "default_to": mail.DefaultNotifyTo})

	case http.MethodPost:
		var in struct {
			Action   string        `json:"action"`
			Settings mail.Settings `json:"settings"`
			To       string        `json:"to"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		cur := store.Load()
		// 未修改时保留原授权码
		if in.Settings.Primary.Password == "" || strings.Contains(in.Settings.Primary.Password, "***") {
			in.Settings.Primary.Password = cur.Primary.Password
		}
		if in.Settings.Backup.Password == "" || strings.Contains(in.Settings.Backup.Password, "***") {
			in.Settings.Backup.Password = cur.Backup.Password
		}

		switch in.Action {
		case "save":
			if err := store.Save(in.Settings); err != nil {
				writeJSON(w, map[string]any{"ok": false, "message": err.Error()})
				return
			}
			writeJSON(w, map[string]any{"ok": true, "message": "邮件设置已保存"})
		case "test":
			to := strings.TrimSpace(in.To)
			if to == "" {
				to = in.Settings.NotifyTo
			}
			if to == "" {
				to = mail.DefaultNotifyTo
			}
			via, err := mail.Test(in.Settings.Primary, to)
			if err != nil {
				store.SetLastTest(err.Error(), false)
				writeJSON(w, map[string]any{"ok": false, "message": "发送失败: " + err.Error()})
				return
			}
			msg := "测试邮件已发送到 " + to + "（" + via + "）"
			store.SetLastTest(msg, true)
			// 保存成功的配置，方便后续复用
			_ = store.Save(in.Settings)
			writeJSON(w, map[string]any{"ok": true, "message": msg})
		default:
			http.Error(w, "未知操作", 400)
		}
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	return "******"
}
