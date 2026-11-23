// Package sip 提供最小 SIP 信令实现：UDP 收发、报文解析/构造、OPTIONS/REGISTER/INVITE 处理。
// 定位是"能跑通呼叫流程的最小内核"，后续可平滑替换为 sipgo 等完整栈。
package sip

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// Message 是解析后的 SIP 报文。
type Message struct {
	StartLine string
	Method    string // 请求方法；响应为空
	StatusCode int
	Headers   map[string][]string
	Body      []byte
}

// Get 读取首个同名头。
func (m *Message) Get(key string) string {
	vs := m.Headers[strings.ToLower(key)]
	if len(vs) == 0 {
		return ""
	}
	return vs[0]
}

// Parse 解析一个完整的 SIP 报文。
func Parse(raw []byte) (*Message, error) {
	br := bufio.NewReader(strings.NewReader(string(raw)))
	start, err := br.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("sip: missing start line: %w", err)
	}
	m := &Message{StartLine: strings.TrimSpace(start), Headers: map[string][]string{}}
	parts := strings.Fields(m.StartLine)
	if len(parts) >= 3 && strings.HasPrefix(parts[0], "SIP/2.0") {
		code, _ := strconv.Atoi(parts[1])
		m.StatusCode = code
	} else if len(parts) >= 1 {
		m.Method = parts[0]
	}

	for {
		line, err := br.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			key := strings.ToLower(strings.TrimSpace(k))
			m.Headers[key] = append(m.Headers[key], strings.TrimSpace(v))
		}
	}
	body, _ := io_ReadAll_limited(br, 64*1024)
	m.Body = body
	return m, nil
}

func io_ReadAll_limited(br *bufio.Reader, max int) ([]byte, error) {
	var out []byte
	buf := make([]byte, 1024)
	for len(out) < max {
		n, err := br.Read(buf)
		if n > 0 {
			out = append(out, buf[:n]...)
		}
		if err != nil {
			break
		}
	}
	return out, nil
}

// Response 构造响应报文。
func Response(req *Message, code int, reason string, extraHeaders map[string]string, body []byte) []byte {
	var b strings.Builder
	b.WriteString("SIP/2.0 " + strconv.Itoa(code) + " " + reason + "\r\n")
	for _, h := range []string{"via", "call-id", "cseq", "from", "to"} {
		for _, v := range req.Headers[h] {
			b.WriteString(title(h) + ": " + v + "\r\n")
		}
	}
	if req.Get("to") != "" && !strings.Contains(req.Get("to"), "tag=") {
		// 简化处理：补 tag 由调用方通过 extraHeaders 覆盖
		_ = 0
	}
	for k, v := range extraHeaders {
		b.WriteString(k + ": " + v + "\r\n")
	}
	b.WriteString("Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n")
	b.Write(body)
	return []byte(b.String())
}

func title(h string) string {
	switch h {
	case "via":
		return "Via"
	case "call-id":
		return "Call-ID"
	case "cseq":
		return "CSeq"
	case "from":
		return "From"
	case "to":
		return "To"
	}
	return h
}

// Server 是一个极简 SIP UAC/UAS 服务端。
type Server struct {
	Addr       string
	RTPPort    int
	OnInvite   func(from, to string, sdp string)
	conn       *net.UDPConn
}

// ListenAndServe 阻塞运行 UDP 服务。
func (s *Server) ListenAndServe(ctx context.Context) error {
	addr, err := net.ResolveUDPAddr("udp", s.Addr)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}
	s.conn = conn
	defer conn.Close()

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	buf := make([]byte, 64*1024)
	for {
		n, raddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return err
			}
		}
		msg, perr := Parse(buf[:n])
		if perr != nil {
			continue
		}
		go s.handle(msg, raddr)
	}
}

func (s *Server) handle(msg *Message, raddr *net.UDPAddr) {
	hdr := map[string]string{
		"To":   withTag(msg.Get("to")),
		"From": msg.Get("from"),
	}
	switch msg.Method {
	case "OPTIONS":
		s.reply(msg, raddr, Response(msg, 200, "OK", hdr, nil))
	case "REGISTER":
		hdr["Expires"] = "3600"
		s.reply(msg, raddr, Response(msg, 200, "OK", hdr, nil))
	case "INVITE":
		sdp := buildSDP(raddr.IP.String(), s.RTPPort)
		s.reply(msg, raddr, Response(msg, 100, "Trying", hdr, nil))
		s.reply(msg, raddr, Response(msg, 180, "Ringing", hdr, nil))
		ok := Response(msg, 200, "OK", merge(hdr, map[string]string{
			"Content-Type": "application/sdp",
		}), []byte(sdp))
		s.reply(msg, raddr, ok)
		if s.OnInvite != nil {
			s.OnInvite(msg.Get("from"), msg.Get("to"), sdp)
		}
	case "ACK":
		// 收到 ACK，媒体通道即可建立
	case "BYE":
		s.reply(msg, raddr, Response(msg, 200, "OK", hdr, nil))
	}
}

func (s *Server) reply(msg *Message, raddr *net.UDPAddr, payload []byte) {
	if s.conn != nil {
		_, _ = s.conn.WriteToUDP(payload, raddr)
	}
}

func merge(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func withTag(to string) string {
	if strings.Contains(to, "tag=") {
		return to
	}
	return to + ";tag=" + strconv.FormatInt(time.Now().UnixNano()%100000, 10)
}

// buildSDP 生成最小 SDP，宣告 G.711 µ-law (PT 0)。
func buildSDP(ip string, rtpPort int) string {
	return fmt.Sprintf("v=0\r\no=- 0 0 IN IP4 %s\r\ns=sipvoice\r\nc=IN IP4 %s\r\nt=0 0\r\n"+
		"m=audio %d RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\na=sendrecv\r\n", ip, ip, rtpPort)
}

// Client 发送一个 OPTIONS 请求并等待响应，用于连通性自检。
func Client(ctx context.Context, target string) (string, error) {
	raddr, err := net.ResolveUDPAddr("udp", target)
	if err != nil {
		return "", err
	}
	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	branch := strconv.FormatInt(time.Now().UnixNano(), 16)
	req := "OPTIONS sip:" + target + " SIP/2.0\r\n" +
		"Via: SIP/2.0/UDP " + conn.LocalAddr().String() + ";branch=z9hG4bK" + branch + "\r\n" +
		"Max-Forwards: 70\r\n" +
		"From: <sip:probe@localhost>;tag=" + branch + "\r\n" +
		"To: <sip:" + target + ">\r\n" +
		"Call-ID: " + branch + "@localhost\r\n" +
		"CSeq: 1 OPTIONS\r\n" +
		"Content-Length: 0\r\n\r\n"

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(3 * time.Second)
	}
	_ = conn.SetDeadline(deadline)

	if _, err := conn.Write([]byte(req)); err != nil {
		return "", err
	}
	buf := make([]byte, 8192)
	n, err := conn.Read(buf)
	if err != nil {
		return "", err
	}
	resp, err := Parse(buf[:n])
	if err != nil {
		return "", err
	}
	return resp.StartLine, nil
}
