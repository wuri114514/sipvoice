// Package mail 提供最小可用的 SMTP 发信能力（仅依赖标准库）。
package mail

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Attach 是一个附件。
type Attach struct {
	Name string
	Data []byte
	MIME string
}

// Config 是 SMTP 配置。
type Config struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	SSL      bool   `json:"ssl"` // true=465 直连 TLS；false=587 STARTTLS
	Username string `json:"username"`
	Password string `json:"password"`
	From     string `json:"from"`
	FromName string `json:"from_name"`
}

// DefaultConfig 返回空配置。
func DefaultConfig() Config {
	return Config{Port: 465, SSL: true}
}

// Normalize 修正明显写错的字段。
func (c *Config) Normalize() {
	c.Host = strings.TrimSpace(c.Host)
	c.Username = strings.TrimSpace(c.Username)
	c.From = strings.TrimSpace(c.From)
	if c.From == "" {
		c.From = c.Username
	}
	if c.Port <= 0 {
		if c.SSL {
			c.Port = 465
		} else {
			c.Port = 587
		}
	}
	// 常见手滑：.con -> .cn
	if strings.HasSuffix(c.Username, ".con") {
		c.Username = strings.TrimSuffix(c.Username, ".con") + ".cn"
		c.From = c.Username
	}
}

// Ready 判断配置是否可用。
func (c Config) Ready() bool {
	return c.Host != "" && c.Username != "" && c.Password != ""
}

// Send 发送一封邮件，失败时自动在 465/587 之间回退。
func Send(cfg Config, to []string, subject, body string, atts []Attach) (string, error) {
	cfg.Normalize()
	if !cfg.Ready() {
		return "", fmt.Errorf("邮件配置不完整（需要服务器/账号/授权码）")
	}
	if len(to) == 0 {
		return "", fmt.Errorf("没有收件人")
	}
	raw := buildMessage(cfg, to, subject, body, atts)

	type attempt struct {
		port int
		ssl  bool
	}
	plans := []attempt{{cfg.Port, cfg.SSL}}
	// 回退尝试另一种方式
	if cfg.SSL {
		plans = append(plans, attempt{587, false})
	} else {
		plans = append(plans, attempt{465, true})
	}

	var lastErr error
	for _, p := range plans {
		err := sendOnce(cfg, to, raw, p.port, p.ssl)
		if err == nil {
			return fmt.Sprintf("%s:%d %s", cfg.Host, p.port, schemeName(p.ssl)), nil
		}
		lastErr = err
	}
	return "", lastErr
}

func schemeName(ssl bool) string {
	if ssl {
		return "SSL"
	}
	return "STARTTLS"
}

// sendOnce 按指定端口/加密方式发一次。
func sendOnce(cfg Config, to []string, raw []byte, port int, ssl bool) error {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(port))
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	tlsCfg := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}

	var (
		conn net.Conn
		err  error
	)
	if ssl {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("连接 %s 失败: %w", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))

	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return fmt.Errorf("SMTP 握手失败: %w", err)
	}
	defer client.Close()

	if !ssl {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("STARTTLS 失败: %w", err)
			}
		}
	}

	// 认证（部分服务器要求先 EHLO，NewClient 已发过）
	auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	if ok, _ := client.Extension("AUTH"); ok {
		if err := client.Auth(auth); err != nil {
			// 有些服务器只支持 LOGIN，PlainAuth 通常也能被接受
			return fmt.Errorf("认证失败（账号或授权码不对）: %w", err)
		}
	}

	if err := client.Mail(cfg.From); err != nil {
		// 有些服务器要求 MAIL FROM 与账号一致
		if err2 := client.Mail(cfg.Username); err2 != nil {
			return fmt.Errorf("发件人被拒: %w", err)
		}
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("收件人 %s 被拒: %w", rcpt, err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA 失败: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("写入正文失败: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("提交失败: %w", err)
	}
	return client.Quit()
}

// buildMessage 组装 MIME 邮件（正文 + 可选附件）。
func buildMessage(cfg Config, to []string, subject, body string, atts []Attach) []byte {
	var buf bytes.Buffer
	fromName := cfg.FromName
	if fromName == "" {
		fromName = "SIPVoice"
	}
	fmt.Fprintf(&buf, "From: %s <%s>\r\n", mime.QEncoding.Encode("utf-8", fromName), cfg.From)
	fmt.Fprintf(&buf, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&buf, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&buf, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&buf, "MIME-Version: 1.0\r\n")

	if len(atts) == 0 {
		fmt.Fprintf(&buf, "Content-Type: text/plain; charset=UTF-8\r\n")
		fmt.Fprintf(&buf, "Content-Transfer-Encoding: quoted-printable\r\n\r\n")
		qp := quotedprintable.NewWriter(&buf)
		_, _ = qp.Write([]byte(body))
		_ = qp.Close()
		return buf.Bytes()
	}

	mw := multipart.NewWriter(&buf)
	fmt.Fprintf(&buf, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", mw.Boundary())

	// 正文部分
	hdr := map[string][]string{
		"Content-Type":              {"text/plain; charset=UTF-8"},
		"Content-Transfer-Encoding": {"quoted-printable"},
	}
	pw, _ := mw.CreatePart(hdr)
	qp := quotedprintable.NewWriter(pw)
	_, _ = qp.Write([]byte(body))
	_ = qp.Close()

	for _, a := range atts {
		mt := a.MIME
		if mt == "" {
			mt = guessMIME(a.Name)
		}
		hasNonASCII := false
		for _, r := range a.Name {
			if r > 127 {
				hasNonASCII = true
				break
			}
		}
		disp := fmt.Sprintf(`attachment; filename="%s"`, a.Name)
		if hasNonASCII {
			disp = fmt.Sprintf(`attachment; filename*=UTF-8''%s`, urlEscape(a.Name))
		}
		ah := map[string][]string{
			"Content-Type":              {fmt.Sprintf("%s; name=\"%s\"", mt, a.Name)},
			"Content-Disposition":       {disp},
			"Content-Transfer-Encoding": {"base64"},
		}
		aw, err := mw.CreatePart(ah)
		if err != nil {
			continue
		}
		enc := base64.StdEncoding.EncodeToString(a.Data)
		for i := 0; i < len(enc); i += 76 {
			end := i + 76
			if end > len(enc) {
				end = len(enc)
			}
			_, _ = aw.Write([]byte(enc[i:end] + "\r\n"))
		}
	}
	_ = mw.Close()
	return buf.Bytes()
}

func urlEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func guessMIME(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf":
		return "application/pdf"
	case ".tiff", ".tif":
		return "image/tiff"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".txt":
		return "text/plain"
	default:
		return "application/octet-stream"
	}
}

// Test 发一封测试邮件。
func Test(cfg Config, to string) (string, error) {
	body := "这是一封 SIPVoice 的测试邮件。\r\n\r\n" +
		"如果你收到这封邮件，说明传真失败通知可以正常发送。\r\n" +
		"时间：" + time.Now().Format("2006-01-02 15:04:05") + "\r\n"
	return Send(cfg, []string{to}, "【SIPVoice】测试邮件", body, nil)
}

// ConvertTIFFToPDF 把 TIFF 转成 PDF（用系统 tiff2pdf），失败则返回错误。
func ConvertTIFFToPDF(src string) ([]byte, error) {
	out := filepath.Join(os.TempDir(), fmt.Sprintf("sipvoice-fax-%d.pdf", time.Now().UnixNano()))
	defer os.Remove(out)
	cmd := exec.Command("tiff2pdf", "-o", out, src)
	if b, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("tiff2pdf 失败: %v %s", err, strings.TrimSpace(string(b)))
	}
	return os.ReadFile(out)
}
