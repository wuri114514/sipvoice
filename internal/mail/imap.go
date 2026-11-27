package mail

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// 这个文件是最小可用的 IMAP4rev1 客户端。
//
// 只用标准库，是因为本项目从 1.x 起就是零第三方依赖：收信只需要
// LOGIN / SELECT / UID SEARCH UNSEEN / UID FETCH / UID STORE(\Seen) 这几条命令，
// 与其引一个完整 IMAP 库，不如把这一小段协议自己实现清楚。
//
// 已知限制：信箱名只支持 ASCII（INBOX 足够用），不做 IDLE 推送，靠定时轮询。

const (
	imapDialTimeout = 20 * time.Second
	imapIOTimeout   = 120 * time.Second
	// 单封邮件体积上限，防止一封超大邮件把内存吃满
	imapMaxLiteral = 32 << 20
)

type imapClient struct {
	conn net.Conn
	r    *bufio.Reader
	tag  int
}

// dialIMAP 建立连接并读掉问候语。
func dialIMAP(cfg ReceiveConfig) (*imapClient, error) {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	d := &net.Dialer{Timeout: imapDialTimeout}

	var conn net.Conn
	var err error
	if cfg.SSL {
		conn, err = tls.DialWithDialer(d, "tcp", addr, &tls.Config{ServerName: cfg.Host})
	} else {
		conn, err = d.Dial("tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("连接 IMAP %s 失败: %w", addr, err)
	}

	c := &imapClient{conn: conn, r: bufio.NewReaderSize(conn, 64<<10)}
	_ = conn.SetDeadline(time.Now().Add(imapIOTimeout))

	line, err := c.readLine()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("读取 IMAP 问候失败: %w", err)
	}
	if !strings.HasPrefix(line, "* OK") && !strings.HasPrefix(line, "* PREAUTH") {
		_ = conn.Close()
		return nil, fmt.Errorf("IMAP 服务端拒绝连接: %s", line)
	}
	return c, nil
}

func (c *imapClient) Close() error { return c.conn.Close() }

func (c *imapClient) readLine() (string, error) {
	_ = c.conn.SetDeadline(time.Now().Add(imapIOTimeout))
	line, err := c.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// do 发一条命令并读到带标签的结束行。
// 返回中间所有的 "*" 响应行，以及遇到的第一段文字面量（邮件的原始字节）。
func (c *imapClient) do(cmd string) ([]string, []byte, error) {
	c.tag++
	tag := fmt.Sprintf("s%d", c.tag)
	_ = c.conn.SetDeadline(time.Now().Add(imapIOTimeout))
	if _, err := io.WriteString(c.conn, tag+" "+cmd+"\r\n"); err != nil {
		return nil, nil, fmt.Errorf("发送 IMAP 命令失败: %w", err)
	}

	var lines []string
	var lit []byte
	for {
		line, err := c.readLine()
		if err != nil {
			return lines, lit, fmt.Errorf("读取 IMAP 响应失败: %w", err)
		}
		if strings.HasPrefix(line, tag+" ") {
			rest := strings.TrimSpace(strings.TrimPrefix(line, tag+" "))
			if strings.HasPrefix(rest, "OK") {
				return lines, lit, nil
			}
			return lines, lit, fmt.Errorf("IMAP %s: %s", cmdName(cmd), rest)
		}
		if n, ok := literalLen(line); ok {
			if n > imapMaxLiteral {
				return lines, lit, fmt.Errorf("邮件体积 %d 字节超过上限 %d", n, imapMaxLiteral)
			}
			buf := make([]byte, n)
			_ = c.conn.SetDeadline(time.Now().Add(imapIOTimeout))
			if _, err := io.ReadFull(c.r, buf); err != nil {
				return lines, lit, fmt.Errorf("读取邮件内容失败: %w", err)
			}
			lit = buf
			// 文字面量之后同一行还有尾巴（通常是 ")"）
			rest, err := c.readLine()
			if err != nil {
				return lines, lit, err
			}
			lines = append(lines, line+rest)
			continue
		}
		lines = append(lines, line)
	}
}

func cmdName(cmd string) string {
	f := strings.Fields(cmd)
	if len(f) == 0 {
		return "?"
	}
	return f[0]
}

// literalLen 判断一行是否以 {NNN} 结尾，返回字面量长度。
func literalLen(line string) (int, bool) {
	i := strings.LastIndex(line, "{")
	if i < 0 || !strings.HasSuffix(line, "}") {
		return 0, false
	}
	n, err := strconv.Atoi(line[i+1 : len(line)-1])
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func imapQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	return `"` + s + `"`
}

func (c *imapClient) login(user, pass string) error {
	_, _, err := c.do("LOGIN " + imapQuote(user) + " " + imapQuote(pass))
	return err
}

// identify 发 ID 命令。网易 163/126 不先发 ID 会以 "Unsafe Login" 拒绝后续操作。
// 服务端不支持 ID 时返回 BAD，忽略即可。
func (c *imapClient) identify() {
	_, _, _ = c.do(`ID ("name" "SIPVoice" "version" "1" "vendor" "sipvoice")`)
}

func (c *imapClient) selectMailbox(name string) error {
	_, _, err := c.do("SELECT " + imapQuote(name))
	return err
}

// searchUnseen 返回所有未读邮件的 UID。
func (c *imapClient) searchUnseen() ([]int, error) {
	lines, _, err := c.do("UID SEARCH UNSEEN")
	if err != nil {
		return nil, err
	}
	var uids []int
	for _, line := range lines {
		if !strings.HasPrefix(strings.ToUpper(line), "* SEARCH") {
			continue
		}
		for _, f := range strings.Fields(line)[2:] {
			if n, err := strconv.Atoi(f); err == nil {
				uids = append(uids, n)
			}
		}
	}
	return uids, nil
}

// fetch 取一封邮件的完整原始内容。
func (c *imapClient) fetch(uid int) ([]byte, error) {
	_, lit, err := c.do(fmt.Sprintf("UID FETCH %d (BODY.PEEK[])", uid))
	if err != nil {
		return nil, err
	}
	if len(lit) == 0 {
		return nil, fmt.Errorf("邮件 %d 内容为空", uid)
	}
	return lit, nil
}

// fetchHeaders 只取 From / Subject 两个头字段。
//
// 收件箱里常有大量与传真无关的通知邮件，它们被跳过后仍然保持未读，
// 会一直占着「前 N 封未读」的位置。先花几十字节看头、命中的才下载正文，
// 这样即使未读邮件很多也不会被堵住。
func (c *imapClient) fetchHeaders(uid int) ([]byte, error) {
	_, lit, err := c.do(fmt.Sprintf("UID FETCH %d (BODY.PEEK[HEADER.FIELDS (FROM SUBJECT)])", uid))
	if err != nil {
		return nil, err
	}
	return lit, nil
}

func (c *imapClient) markSeen(uids []int) error {
	for _, uid := range uids {
		if _, _, err := c.do(fmt.Sprintf("UID STORE %d +FLAGS (\\Seen)", uid)); err != nil {
			return err
		}
	}
	return nil
}

func (c *imapClient) logout() {
	_, _, _ = c.do("LOGOUT")
}

// withIMAP 建立连接、登录、执行回调、登出。
func withIMAP(cfg ReceiveConfig, fn func(*imapClient) error) error {
	cfg.Normalize()
	if !cfg.Ready() {
		return fmt.Errorf("收信配置不完整（需要 IMAP 服务器/账号/授权码）")
	}
	c, err := dialIMAP(cfg)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.login(cfg.Username, cfg.Password); err != nil {
		return fmt.Errorf("IMAP 登录失败（QQ/163 要用授权码，不是登录密码）: %w", err)
	}
	defer c.logout()
	c.identify()
	if err := c.selectMailbox(cfg.Mailbox); err != nil {
		return fmt.Errorf("打开信箱 %s 失败: %w", cfg.Mailbox, err)
	}
	return fn(c)
}

// FetchUnseen 拉取未读邮件（最多 limit 封），并把原始内容解析成 Incoming。
// 注意：这里不会把邮件标记为已读——调用方处理完再调 MarkSeen，避免丢信。
func FetchUnseen(cfg ReceiveConfig, limit int) ([]Incoming, error) {
	if limit <= 0 {
		limit = 10
	}
	var out []Incoming
	err := withIMAP(cfg, func(c *imapClient) error {
		uids, err := c.searchUnseen()
		if err != nil {
			return err
		}
		if len(uids) > limit {
			uids = uids[:limit]
		}
		for _, uid := range uids {
			raw, err := c.fetch(uid)
			if err != nil {
				// 单封失败不影响其它邮件
				continue
			}
			m, err := ParseMessage(uid, raw, cfg.MaxAttachMB)
			if err != nil {
				continue
			}
			out = append(out, *m)
		}
		return nil
	})
	return out, err
}

// MarkSeen 把处理过的邮件标记为已读（下次轮询不会再取到）。
func MarkSeen(cfg ReceiveConfig, uids []int) error {
	if len(uids) == 0 {
		return nil
	}
	return withIMAP(cfg, func(c *imapClient) error { return c.markSeen(uids) })
}

// CheckReceive 只做一次登录 + 统计未读数，供控制台「测试收信」用。
func CheckReceive(cfg ReceiveConfig) (int, error) {
	n := 0
	err := withIMAP(cfg, func(c *imapClient) error {
		uids, err := c.searchUnseen()
		if err != nil {
			return err
		}
		n = len(uids)
		return nil
	})
	return n, err
}
