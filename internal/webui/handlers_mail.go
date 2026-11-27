package webui

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"sipvoice/internal/mail"
)

// handleMail 读取/保存邮件设置，发送测试邮件，测试收信。
func (s *Server) handleMail(w http.ResponseWriter, r *http.Request) {
	store := s.mailStore()

	switch r.Method {
	case http.MethodGet:
		st := store.Load()
		// 不回传授权码明文，只标记是否已设置
		st.Primary.Password = maskSecret(st.Primary.Password)
		st.Backup.Password = maskSecret(st.Backup.Password)
		st.Receive.Password = maskSecret(st.Receive.Password)
		writeJSON(w, map[string]any{"ok": true, "settings": st, "default_to": mail.DefaultNotifyTo})

	case http.MethodPost:
		// 先把原始 JSON 留着：要判断请求里到底带了哪几块配置，
		// 缺块不能当成“清空”（老版本页面/部分提交会把整块写成零值）。
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		var env struct {
			Action   string          `json:"action"`
			Settings json.RawMessage `json:"settings"`
			To       string          `json:"to"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			http.Error(w, "配置解析失败: "+err.Error(), 400)
			return
		}

		var in mail.Settings
		if len(env.Settings) > 0 {
			if err := json.Unmarshal(env.Settings, &in); err != nil {
				http.Error(w, "配置解析失败: "+err.Error(), 400)
				return
			}
		}

		cur := store.Load()
		present := map[string]json.RawMessage{}
		_ = json.Unmarshal(env.Settings, &present)
		if _, ok := present["primary"]; !ok {
			in.Primary = cur.Primary
		}
		if _, ok := present["backup"]; !ok {
			in.Backup = cur.Backup
		}
		if _, ok := present["receive"]; !ok {
			in.Receive = cur.Receive
		}

		// 未修改时保留原授权码
		if in.Primary.Password == "" || strings.Contains(in.Primary.Password, "***") {
			in.Primary.Password = cur.Primary.Password
		}
		if in.Backup.Password == "" || strings.Contains(in.Backup.Password, "***") {
			in.Backup.Password = cur.Backup.Password
		}
		if in.Receive.Password == "" || strings.Contains(in.Receive.Password, "***") {
			in.Receive.Password = cur.Receive.Password
		}
		// 轮询状态由后台写入，面板保存时不该把它清掉
		if in.Receive.LastPoll == "" {
			in.Receive.LastPoll = cur.Receive.LastPoll
			in.Receive.LastResult = cur.Receive.LastResult
		}

		switch env.Action {
		case "save":
			if err := store.Save(in); err != nil {
				log.Printf("[webui] 保存邮件设置失败: %v", err)
				writeJSON(w, map[string]any{"ok": false, "message": "保存失败: " + err.Error()})
				return
			}
			log.Printf("[webui] 邮件设置已保存（发信 %s，收信 %s，白名单 %d 个）",
				describeSMTP(in.Primary), describeIMAP(in.Receive), len(in.Receive.FromAllow))
			writeJSON(w, map[string]any{"ok": true, "message": "邮件设置已保存"})

		case "test":
			// 先落盘再做测试：用户填完点按钮，输入不该因为测试失败而丢掉
			if err := store.Save(in); err != nil {
				log.Printf("[webui] 保存邮件设置失败（继续测试）: %v", err)
			}
			to := strings.TrimSpace(env.To)
			if to == "" {
				to = in.NotifyTo
			}
			if to == "" {
				to = mail.DefaultNotifyTo
			}
			via, err := mail.Test(in.Primary, to)
			if err != nil {
				store.SetLastTest(err.Error(), false)
				log.Printf("[webui] 测试邮件失败: %v", err)
				writeJSON(w, map[string]any{"ok": false, "message": "发送失败: " + err.Error()})
				return
			}
			msg := "测试邮件已发送到 " + to + "（" + via + "）"
			store.SetLastTest(msg, true)
			log.Printf("[webui] %s", msg)
			writeJSON(w, map[string]any{"ok": true, "message": msg})

		case "test-receive":
			// 同样先落盘：IMAP 连不上时，用户填的服务器/账号也要留下来
			if err := store.Save(in); err != nil {
				log.Printf("[webui] 保存收信设置失败（继续测试）: %v", err)
			}
			n, err := mail.CheckReceive(in.Receive)
			if err != nil {
				log.Printf("[webui] 测试收信失败（%s）: %v", describeIMAP(in.Receive), err)
				writeJSON(w, map[string]any{"ok": false, "message": "收信测试失败: " + err.Error()})
				return
			}
			msg := fmt.Sprintf("收信正常，当前未读 %d 封（%s@%s）",
				n, in.Receive.Username, in.Receive.Host)
			log.Printf("[webui] %s", msg)
			writeJSON(w, map[string]any{"ok": true, "message": msg})

		default:
			http.Error(w, "未知操作", 400)
		}
	default:
		http.Error(w, "method not allowed", 405)
	}
}

// describeSMTP 生成不含密码的日志摘要。
func describeSMTP(c mail.Config) string {
	if c.Host == "" {
		return "未配置"
	}
	user := c.Username
	if user == "" {
		user = "-"
	}
	return fmt.Sprintf("%s:%d/%s", c.Host, c.Port, user)
}

// describeIMAP 生成不含密码的日志摘要。
func describeIMAP(r mail.ReceiveConfig) string {
	if r.Host == "" {
		return "未启用"
	}
	return fmt.Sprintf("%s:%d/%s", r.Host, r.Port, r.Username)
}

func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	return "******"
}
