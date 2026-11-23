// Package ws 是一个极简的 RFC6455 WebSocket 客户端。
//
// 只实现本项目需要的能力：文本帧收发、分片重组、ping/pong、关闭握手。
// 不引入第三方依赖，便于纯离线构建。
package ws

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"sipvoice/internal/netutil"
)

const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA

	maxMessage = 32 << 20
	wsGUID     = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
)

// Client 是一条已建立的 WebSocket 连接。
type Client struct {
	conn net.Conn
	br   *bufio.Reader

	wmu sync.Mutex
}

// Dial 建立连接；hdr 里的键会附加到握手请求上（如 Authorization）。
func Dial(ctx context.Context, rawurl string, hdr http.Header) (*Client, error) {
	u, err := url.Parse(rawurl)
	if err != nil {
		return nil, fmt.Errorf("解析地址: %w", err)
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		if u.Scheme == "wss" {
			host += ":443"
		} else {
			host += ":80"
		}
	}

	var conn net.Conn
	switch u.Scheme {
	case "wss":
		conn, err = netutil.DialTLSContext(ctx, "tcp", host, &tls.Config{})
	case "ws":
		conn, err = netutil.DialContext(ctx, "tcp", host)
	default:
		return nil, fmt.Errorf("不支持的 scheme: %s", u.Scheme)
	}
	if err != nil {
		return nil, fmt.Errorf("连接 %s: %w", host, err)
	}

	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		conn.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)

	path := u.RequestURI()
	if path == "" {
		path = "/"
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "GET %s HTTP/1.1\r\n", path)
	fmt.Fprintf(&sb, "Host: %s\r\n", u.Host)
	sb.WriteString("Upgrade: websocket\r\n")
	sb.WriteString("Connection: Upgrade\r\n")
	fmt.Fprintf(&sb, "Sec-WebSocket-Key: %s\r\n", key)
	sb.WriteString("Sec-WebSocket-Version: 13\r\n")
	for k, vs := range hdr {
		for _, v := range vs {
			fmt.Fprintf(&sb, "%s: %s\r\n", k, v)
		}
	}
	sb.WriteString("\r\n")

	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	if _, err := io.WriteString(conn, sb.String()); err != nil {
		conn.Close()
		return nil, fmt.Errorf("握手写入: %w", err)
	}
	br := bufio.NewReaderSize(conn, 64<<10)
	req := &http.Request{Method: http.MethodGet}
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("握手响应: %w", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		conn.Close()
		return nil, fmt.Errorf("握手失败 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	want := acceptKey(key)
	if got := resp.Header.Get("Sec-WebSocket-Accept"); got != want {
		conn.Close()
		return nil, fmt.Errorf("Sec-WebSocket-Accept 不匹配")
	}
	_ = conn.SetDeadline(time.Time{})
	return &Client{conn: conn, br: br}, nil
}

// WriteText 发送一条文本消息。
func (c *Client) WriteText(payload []byte) error {
	return c.writeFrame(opText, payload)
}

// WritePing 发送 ping。
func (c *Client) WritePing() error {
	return c.writeFrame(opPing, nil)
}

// ReadMessage 读取一条完整消息（自动重组分片、自动回 pong）。
// 返回 opcode 与负载。调用方应设置 ctx 超时来控制阻塞。
func (c *Client) ReadMessage(ctx context.Context) (int, []byte, error) {
	type result struct {
		op   int
		data []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		op, data, err := c.readMessageBlocking()
		ch <- result{op, data, err}
	}()
	select {
	case r := <-ch:
		return r.op, r.data, r.err
	case <-ctx.Done():
		c.conn.Close()
		r := <-ch
		if r.err != nil {
			return 0, nil, ctx.Err()
		}
		return r.op, r.data, ctx.Err()
	}
}

func (c *Client) readMessageBlocking() (int, []byte, error) {
	var (
		msgOp   int
		buf     []byte
		started bool
	)
	for {
		fin, op, payload, err := c.readFrame()
		if err != nil {
			return 0, nil, err
		}
		switch op {
		case opPing:
			_ = c.writeFrame(opPong, payload)
			continue
		case opPong:
			continue
		case opClose:
			_ = c.writeFrame(opClose, payload)
			return opClose, nil, io.EOF
		case opContinuation:
			if !started {
				return 0, nil, fmt.Errorf("非法续帧")
			}
			buf = append(buf, payload...)
		default:
			if started && op != msgOp {
				return 0, nil, fmt.Errorf("消息中混入新 opcode")
			}
			msgOp = op
			started = true
			buf = append(buf, payload...)
		}
		if len(buf) > maxMessage {
			return 0, nil, fmt.Errorf("消息过大")
		}
		if fin && started {
			return msgOp, buf, nil
		}
	}
}

func (c *Client) readFrame() (fin bool, op int, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(c.br, h[:]); err != nil {
		return
	}
	fin = h[0]&0x80 != 0
	op = int(h[0] & 0x0F)
	masked := h[1]&0x80 != 0
	n := int64(h[1] & 0x7F)
	switch n {
	case 126:
		var b [2]byte
		if _, err = io.ReadFull(c.br, b[:]); err != nil {
			return
		}
		n = int64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err = io.ReadFull(c.br, b[:]); err != nil {
			return
		}
		n = int64(binary.BigEndian.Uint64(b[:]))
	}
	if n < 0 || n > maxMessage {
		return fin, op, nil, fmt.Errorf("帧长度异常: %d", n)
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(c.br, mask[:]); err != nil {
			return
		}
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(c.br, payload); err != nil {
		return
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return fin, op, payload, nil
}

func (c *Client) writeFrame(op int, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()

	var hdr [14]byte
	hdr[0] = 0x80 | byte(op)
	n := len(payload)
	pos := 2
	switch {
	case n < 126:
		hdr[1] = 0x80 | byte(n)
	case n <= 0xFFFF:
		hdr[1] = 0x80 | 126
		binary.BigEndian.PutUint16(hdr[2:], uint16(n))
		pos = 4
	default:
		hdr[1] = 0x80 | 127
		binary.BigEndian.PutUint64(hdr[2:], uint64(n))
		pos = 10
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	copy(hdr[pos:], mask[:])
	pos += 4

	frame := make([]byte, pos+n)
	copy(frame, hdr[:pos])
	for i := 0; i < n; i++ {
		frame[pos+i] = payload[i] ^ mask[i%4]
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(20 * time.Second))
	_, err := c.conn.Write(frame)
	return err
}

// Close 发送关闭帧并关闭连接。
func (c *Client) Close() error {
	_ = c.writeFrame(opClose, []byte{0x03, 0xE8})
	return c.conn.Close()
}

func acceptKey(key string) string {
	h := sha1.New()
	io.WriteString(h, key)
	io.WriteString(h, wsGUID)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}
