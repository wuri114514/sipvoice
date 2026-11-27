package mail

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeIMAP 是一个只认几条命令的假 IMAP 服务端，用来验证客户端的
// 登录 → SELECT → SEARCH UNSEEN → FETCH → STORE \Seen 流程。
// 顺手模拟「不支持 ID 命令」的服务端（返回 BAD），确认客户端能容错。
type fakeIMAP struct {
	ln   net.Listener
	raw  []byte
	mu   sync.Mutex
	cmds []string
	seen []int
}

func newFakeIMAP(t *testing.T, raw []byte) *fakeIMAP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeIMAP{ln: ln, raw: raw}
	go f.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *fakeIMAP) hostPort(t *testing.T) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(f.ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	return host, port
}

func (f *fakeIMAP) serve() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(c)
	}
}

func (f *fakeIMAP) handle(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	fmt.Fprint(c, "* OK fake IMAP ready\r\n")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		parts := strings.SplitN(line, " ", 2)
		if len(parts) < 2 {
			return
		}
		tag, cmd := parts[0], parts[1]
		f.mu.Lock()
		f.cmds = append(f.cmds, cmd)
		f.mu.Unlock()

		up := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(up, "LOGIN"):
			fmt.Fprintf(c, "%s OK LOGIN completed\r\n", tag)
		case strings.HasPrefix(up, "ID "):
			fmt.Fprintf(c, "%s BAD unknown command\r\n", tag)
		case strings.HasPrefix(up, "SELECT"):
			fmt.Fprintf(c, "* 1 EXISTS\r\n* OK [UIDVALIDITY 1] uids\r\n%s OK [READ-WRITE] SELECT completed\r\n", tag)
		case strings.HasPrefix(up, "UID SEARCH"):
			fmt.Fprintf(c, "* SEARCH 7\r\n%s OK SEARCH completed\r\n", tag)
		case strings.HasPrefix(up, "UID FETCH"):
			fmt.Fprintf(c, "* 1 FETCH (UID 7 BODY[] {%d}\r\n", len(f.raw))
			_, _ = c.Write(f.raw)
			fmt.Fprintf(c, ")\r\n%s OK FETCH completed\r\n", tag)
		case strings.HasPrefix(up, "UID STORE"):
			fields := strings.Fields(cmd)
			if len(fields) >= 3 {
				if n, err := strconv.Atoi(fields[2]); err == nil {
					f.mu.Lock()
					f.seen = append(f.seen, n)
					f.mu.Unlock()
				}
			}
			fmt.Fprintf(c, "%s OK STORE completed\r\n", tag)
		case strings.HasPrefix(up, "LOGOUT"):
			fmt.Fprintf(c, "* BYE\r\n%s OK LOGOUT completed\r\n", tag)
			return
		default:
			fmt.Fprintf(c, "%s OK done\r\n", tag)
		}
	}
}

func testRawMessage(t *testing.T) []byte {
	t.Helper()
	img := []byte("fake-image-payload")
	raw := strings.Join([]string{
		"From: Alice <alice@example.com>",
		"Subject: =?utf-8?B?" + base64.StdEncoding.EncodeToString([]byte("[FAX] 扫描件")) + "?=",
		`Content-Type: multipart/mixed; boundary="B"`,
		"",
		"--B",
		"Content-Type: text/plain",
		"",
		"请发到 [2002]",
		"--B",
		"Content-Type: image/png; name=\"scan.png\"",
		`Content-Disposition: attachment; filename="scan.png"`,
		"Content-Transfer-Encoding: base64",
		"",
		base64.StdEncoding.EncodeToString(img),
		"--B--",
		"",
	}, "\r\n")
	return []byte(raw)
}

func fakeConfig(t *testing.T, f *fakeIMAP) ReceiveConfig {
	t.Helper()
	host, port := f.hostPort(t)
	cfg := DefaultReceiveConfig()
	cfg.Host = host
	cfg.Port = port
	cfg.SSL = false
	cfg.Username = "fax@example.com"
	cfg.Password = "authcode"
	cfg.Mailbox = "INBOX"
	return cfg
}

func TestFetchUnseen(t *testing.T) {
	f := newFakeIMAP(t, testRawMessage(t))
	cfg := fakeConfig(t, f)

	msgs, err := FetchUnseen(cfg, 5)
	if err != nil {
		t.Fatalf("FetchUnseen: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("取到 %d 封，want 1", len(msgs))
	}
	m := msgs[0]
	if m.UID != 7 {
		t.Errorf("UID = %d, want 7", m.UID)
	}
	if !strings.Contains(m.Subject, "[FAX]") {
		t.Errorf("主题 = %q", m.Subject)
	}
	if ExtractDest(m.Body) != "2002" {
		t.Errorf("正文 = %q", m.Body)
	}
	if len(m.Attachments) != 1 || m.Attachments[0].Name != "scan.png" {
		t.Errorf("附件 = %+v", m.Attachments)
	}

	// ID 命令被服务端以 BAD 拒绝，不能影响后续流程
	f.mu.Lock()
	cmds := strings.Join(f.cmds, " | ")
	f.mu.Unlock()
	if !strings.Contains(cmds, "ID ") {
		t.Errorf("客户端应尝试发送 ID 命令: %s", cmds)
	}
}

func TestMarkSeen(t *testing.T) {
	f := newFakeIMAP(t, testRawMessage(t))
	cfg := fakeConfig(t, f)

	if err := MarkSeen(cfg, []int{7}); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.seen) != 1 || f.seen[0] != 7 {
		t.Fatalf("服务端收到的 STORE = %v, want [7]", f.seen)
	}
}

func TestCheckReceive(t *testing.T) {
	f := newFakeIMAP(t, testRawMessage(t))
	cfg := fakeConfig(t, f)

	n, err := CheckReceive(cfg)
	if err != nil {
		t.Fatalf("CheckReceive: %v", err)
	}
	if n != 1 {
		t.Errorf("未读数 = %d, want 1", n)
	}
}

func TestReceiveConfigIncomplete(t *testing.T) {
	if _, err := CheckReceive(DefaultReceiveConfig()); err == nil {
		t.Error("空配置应该报错")
	}
}
