package webui

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"sipvoice/internal/mail"
)

func newMailServer(t *testing.T) (*Server, *mail.Store) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mail.json")
	return &Server{opt: Options{MailPath: p}}, &mail.Store{Path: p}
}

func postMail(t *testing.T, s *Server, body any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/mail", bytes.NewReader(raw))
	w := httptest.NewRecorder()
	s.handleMail(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func fullSettings() map[string]any {
	return map[string]any{
		"enabled":   true,
		"notify_to": "a@b.com",
		"reply_pdf": true,
		"primary": map[string]any{
			"host": "smtp.example.com", "port": 465, "ssl": true,
			"username": "u@example.com", "password": "p1", "from": "u@example.com", "from_name": "SIPVoice",
		},
		"receive": map[string]any{
			"enabled": true, "host": "imap.example.com", "port": 993, "ssl": true,
			"username": "u@example.com", "password": "p2", "mailbox": "INBOX",
			"poll_seconds": 30, "from_allow": []string{"boss@example.com"},
			"subject_tag": "[FAX]", "mark_seen": true, "max_attach_mb": 10,
		},
	}
}

// 点「保存」必须真的落盘，并且读回来一致。
func TestMailSavePersists(t *testing.T) {
	s, store := newMailServer(t)
	code, out := postMail(t, s, map[string]any{"action": "save", "settings": fullSettings()})
	if code != 200 || out["ok"] != true {
		t.Fatalf("保存失败: code=%d out=%v", code, out)
	}
	got := store.Load()
	if got.Primary.Host != "smtp.example.com" {
		t.Errorf("SMTP 主机没存上: %+v", got.Primary)
	}
	if got.Receive.Host != "imap.example.com" || !got.Receive.Enabled {
		t.Errorf("收信配置没存上: %+v", got.Receive)
	}
	if got.Receive.Password != "p2" || got.Primary.Password != "p1" {
		t.Errorf("授权码没存上")
	}
	if len(got.Receive.FromAllow) != 1 || got.Receive.FromAllow[0] != "boss@example.com" {
		t.Errorf("白名单没存上: %v", got.Receive.FromAllow)
	}
}

// 老页面（提交体里没有 receive 块）不能把已存的收信配置清零。
func TestMailSaveKeepsMissingBlocks(t *testing.T) {
	s, store := newMailServer(t)
	if code, out := postMail(t, s, map[string]any{"action": "save", "settings": fullSettings()}); code != 200 || out["ok"] != true {
		t.Fatalf("首次保存失败")
	}

	// 模拟旧版前端：只带 primary，没有 receive
	old := map[string]any{
		"action": "save",
		"settings": map[string]any{
			"enabled":   true,
			"notify_to": "new@b.com",
			"primary": map[string]any{
				"host": "smtp2.example.com", "port": 587, "ssl": false,
				"username": "u@example.com", "password": "******",
			},
		},
	}
	if code, out := postMail(t, s, old); code != 200 || out["ok"] != true {
		t.Fatalf("二次保存失败: %d %v", code, out)
	}

	got := store.Load()
	if got.Primary.Host != "smtp2.example.com" {
		t.Errorf("SMTP 主机应更新: %+v", got.Primary)
	}
	if got.Primary.Password != "p1" {
		t.Errorf("掩码密码应保留原值，实际 %q", got.Primary.Password)
	}
	if got.Receive.Host != "imap.example.com" || got.Receive.Password != "p2" {
		t.Errorf("缺少 receive 块时应保留原收信配置: %+v", got.Receive)
	}
	if got.NotifyTo != "new@b.com" {
		t.Errorf("notify_to 应更新")
	}
}

// 「测试收信」失败时，用户填的配置也要先落盘（以前失败就直接 return，输入全丢）。
func TestMailTestReceiveSavesEvenWhenConnectFails(t *testing.T) {
	s, store := newMailServer(t)
	settings := fullSettings()
	rc := settings["receive"].(map[string]any)
	rc["host"] = "127.0.0.1"
	rc["port"] = 1
	rc["ssl"] = false

	code, out := postMail(t, s, map[string]any{"action": "test-receive", "settings": settings})
	if code != 200 {
		t.Fatalf("HTTP %d", code)
	}
	if out["ok"] != false {
		t.Fatalf("连不上却报成功: %v", out)
	}
	got := store.Load()
	if got.Receive.Host != "127.0.0.1" || got.Receive.Username != "u@example.com" {
		t.Errorf("测试失败也必须保存用户填的配置: %+v", got.Receive)
	}
}
