package mail

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestParseMessageMultipart(t *testing.T) {
	img := []byte("fake-png-bytes-0123456789")
	subj := "=?utf-8?B?" + base64.StdEncoding.EncodeToString([]byte("[FAX] 扫描件")) + "?="
	raw := strings.Join([]string{
		"From: Alice <Alice@Example.com>",
		"To: fax@example.com",
		"Subject: " + subj,
		"MIME-Version: 1.0",
		`Content-Type: multipart/mixed; boundary="B"`,
		"",
		"--B",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"请发到 [2002] 谢谢",
		"--B",
		`Content-Type: image/png; name="scan.png"`,
		`Content-Disposition: attachment; filename="scan.png"`,
		"Content-Transfer-Encoding: base64",
		"",
		base64.StdEncoding.EncodeToString(img),
		"--B--",
		"",
	}, "\r\n")

	m, err := ParseMessage(7, []byte(raw), 10)
	if err != nil {
		t.Fatalf("ParseMessage: %v", err)
	}
	if m.From != "alice@example.com" {
		t.Errorf("From = %q", m.From)
	}
	if !strings.Contains(m.Subject, "[FAX]") {
		t.Errorf("Subject 解码失败: %q", m.Subject)
	}
	if !strings.Contains(m.Body, "[2002]") {
		t.Errorf("Body = %q", m.Body)
	}
	if len(m.Attachments) != 1 {
		t.Fatalf("附件数 = %d, want 1", len(m.Attachments))
	}
	a := m.Attachments[0]
	if a.Name != "scan.png" || a.MIME != "image/png" {
		t.Errorf("附件元信息不对: %+v", a)
	}
	if !bytes.Equal(a.Data, img) {
		t.Errorf("附件内容解 base64 后不一致")
	}
}

// 只有 HTML 正文的邮件，也要能取到里面的号码。
func TestParseMessageHTMLOnly(t *testing.T) {
	raw := strings.Join([]string{
		"From: bob@example.com",
		"Subject: [FAX] 转发",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<html><body><p>目标&nbsp;[1003]</p><p>谢谢</p></body></html>",
	}, "\r\n")

	m, err := ParseMessage(1, []byte(raw), 10)
	if err != nil {
		t.Fatalf("ParseMessage: %v", err)
	}
	if !strings.Contains(m.Body, "[1003]") {
		t.Errorf("HTML 正文没转出号码: %q", m.Body)
	}
	if strings.Contains(m.Body, "<p>") {
		t.Errorf("HTML 标签没清掉: %q", m.Body)
	}
}

// 非图片附件（比如 txt）不应被当成传真页面。
func TestParseMessageSkipsNonImageAttachment(t *testing.T) {
	raw := strings.Join([]string{
		"From: bob@example.com",
		"Subject: [FAX]",
		`Content-Type: multipart/mixed; boundary="B"`,
		"",
		"--B",
		"Content-Type: text/plain",
		"",
		"[2002]",
		"--B",
		"Content-Type: text/plain; name=\"note.txt\"",
		`Content-Disposition: attachment; filename="note.txt"`,
		"",
		"attachment text",
		"--B--",
		"",
	}, "\r\n")

	m, err := ParseMessage(2, []byte(raw), 10)
	if err != nil {
		t.Fatalf("ParseMessage: %v", err)
	}
	if len(m.Attachments) != 0 {
		t.Errorf("txt 附件不应被收集: %+v", m.Attachments)
	}
	if !strings.Contains(m.Body, "[2002]") {
		t.Errorf("正文应保留 text/plain 分段: %q", m.Body)
	}
}

// 超过体积上限的附件直接丢弃，避免把内存吃穿。
func TestParseMessageDropsOversizeAttachment(t *testing.T) {
	big := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("x"), 2048))
	raw := strings.Join([]string{
		"From: bob@example.com",
		"Subject: [FAX]",
		`Content-Type: multipart/mixed; boundary="B"`,
		"",
		"--B",
		"Content-Type: text/plain",
		"",
		"[2002]",
		"--B",
		"Content-Type: image/png; name=\"big.png\"",
		`Content-Disposition: attachment; filename="big.png"`,
		"Content-Transfer-Encoding: base64",
		"",
		big,
		"--B--",
		"",
	}, "\r\n")

	// 上限 1MB -> 这封只有 2KB，应该保留
	m, err := ParseMessage(3, []byte(raw), 1)
	if err != nil {
		t.Fatalf("ParseMessage: %v", err)
	}
	if len(m.Attachments) != 1 {
		t.Fatalf("2KB 附件在 1MB 上限下应保留，实际 %d", len(m.Attachments))
	}
}

// TestParseMessageOctetStreamImage 覆盖 Foxmail/Outlook 的内嵌图片形态：
// 附件类型是 application/octet-stream、文件名是 Content-ID（无扩展名），
// 必须按文件头识别出真实类型并作为传真页收集。
func TestParseMessageOctetStreamImage(t *testing.T) {
	// 最小 PNG 头 + 填充
	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 64))
	cid := "05D899A1@33F2A96C.9748096B00000000"
	raw := strings.Join([]string{
		"From: Alice <alice@example.com>",
		"To: fax@example.com",
		"Subject: [FAX]",
		"MIME-Version: 1.0",
		`Content-Type: multipart/mixed; boundary="B"`,
		"",
		"--B",
		"Content-Type: text/plain; charset=ISO-8859-1",
		"Content-Transfer-Encoding: base64",
		"",
		base64.StdEncoding.EncodeToString([]byte("[2002]")),
		"--B",
		`Content-Type: application/octet-stream; name="` + cid + `"`,
		`Content-ID: <` + cid + `>`,
		"Content-Transfer-Encoding: base64",
		"",
		base64.StdEncoding.EncodeToString(png),
		"--B--",
		"",
	}, "\r\n")

	m, err := ParseMessage(42, []byte(raw), 10)
	if err != nil {
		t.Fatalf("ParseMessage: %v", err)
	}
	if got := ExtractDest(m.Body); got != "2002" {
		t.Errorf("目标号码 = %q，正文 = %q", got, m.Body)
	}
	if len(m.Attachments) != 1 {
		t.Fatalf("附件数 = %d，期望 1", len(m.Attachments))
	}
	if m.Attachments[0].MIME != "image/png" {
		t.Errorf("嗅探出的类型 = %q，期望 image/png", m.Attachments[0].MIME)
	}
	if !strings.HasSuffix(m.Attachments[0].Name, ".png") {
		t.Errorf("补的扩展名不对: %q", m.Attachments[0].Name)
	}
}

// TestBriefHeaderDecodesSender 校验只取头部时也能拿到纯地址。
// 头部常见形态是 "=?utf-8?B?...?=" <a@b.com>，直接比原始字符串会永远匹配不上白名单。
func TestBriefHeaderDecodesSender(t *testing.T) {
	raw := strings.Join([]string{
		`From: "=?ISO-8859-1?B?QWxpY2U=?=" <alice@example.com>`,
		"Subject: [FAX]",
		"",
		"",
	}, "\r\n")
	subj, from := briefHeader([]byte(raw))
	if from != "alice@example.com" {
		t.Errorf("发件人 = %q，期望 alice@example.com", from)
	}
	if subj != "[FAX]" {
		t.Errorf("主题 = %q", subj)
	}
	cfg := ReceiveConfig{FromAllow: []string{"alice@example.com"}, SubjectTag: "[FAX]"}
	if !cfg.SubjectIsFax(subj) {
		t.Error("主题判定应命中")
	}
	if !cfg.SenderAllowed(from) {
		t.Error("发件人判定应命中")
	}
}
