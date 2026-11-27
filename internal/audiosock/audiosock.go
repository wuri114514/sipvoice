// Package audiosock 实现 Asterisk AudioSocket 协议的服务端：
// 每条 TCP 连接对应一通电话，双向传输 8kHz/16-bit 单声道 PCM。
//
// 协议：3 字节头（type + 2 字节大端长度）+ 负载。
// 连接建立后 Asterisk 先发 0x01（16 字节 UUID），随后发 0x10（音频）、0x03（DTMF）、0x00（挂断）。
package audiosock

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	TypeHangup byte = 0x00
	TypeUUID   byte = 0x01
	TypeDTMF   byte = 0x03
	TypeSlin8k byte = 0x10
)

// FrameBytes 是一帧 20ms、8kHz、16-bit 单声道 PCM 的字节数。
const FrameBytes = 320

// Frame 是一条 AudioSocket 消息。
type Frame struct {
	Type    byte
	Payload []byte
}

// ReadFrame 读取一条消息。
func ReadFrame(r io.Reader) (Frame, error) {
	var h [3]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return Frame{}, err
	}
	n := int(binary.BigEndian.Uint16(h[1:3]))
	f := Frame{Type: h[0]}
	if n > 0 {
		f.Payload = make([]byte, n)
		if _, err := io.ReadFull(r, f.Payload); err != nil {
			return Frame{}, err
		}
	}
	return f, nil
}

// WriteFrame 写一条消息。
func WriteFrame(w io.Writer, typ byte, payload []byte) error {
	// 头和负载必须一次写入：Asterisk 的 res_audiosocket 在某些版本里会按整帧读取，
	// 分成两次 write 会产生两个 TCP 段，可能被判定为帧不完整导致通话被挂断。
	buf := make([]byte, 3+len(payload))
	buf[0] = typ
	buf[1] = byte(len(payload) >> 8)
	buf[2] = byte(len(payload))
	copy(buf[3:], payload)
	_, err := w.Write(buf)
	return err
}

// Conn 是一通电话的 AudioSocket 连接。
type Conn struct {
	UUID string
	// Caller 是主叫号码（来自 Asterisk 的 CALLERID），用于按号码归并会话
	Caller string
	Start  time.Time
	rw     net.Conn
	mu     sync.Mutex

	FramesIn  int64
	FramesOut int64
	BytesIn   int64
	BytesOut  int64

	// lastWrite 是最后一次成功写帧的时间戳（纳秒），用于静音填充判断。
	lastWrite atomic.Int64
}

// LastWrite 返回最后一次写帧的时间。
func (c *Conn) LastWrite() time.Time {
	return time.Unix(0, c.lastWrite.Load())
}

// WriteAudio 发送一帧 8kHz PCM。
func (c *Conn) WriteAudio(pcm []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := WriteFrame(c.rw, TypeSlin8k, pcm); err != nil {
		return err
	}
	c.FramesOut++
	c.BytesOut += int64(len(pcm))
	c.lastWrite.Store(time.Now().UnixNano())
	return nil
}

// Hangup 主动挂断：先发 0x00 再关闭。
func (c *Conn) Hangup() {
	c.mu.Lock()
	_ = WriteFrame(c.rw, TypeHangup, nil)
	c.mu.Unlock()
	_ = c.rw.Close()
}

// Close 关闭连接。
func (c *Conn) Close() error { return c.rw.Close() }

// SetReadDeadline 设置读超时。
func (c *Conn) SetReadDeadline(t time.Time) error { return c.rw.SetReadDeadline(t) }

// ReadFrame 读取一条消息并累计统计。
func (c *Conn) ReadFrame() (Frame, error) {
	f, err := ReadFrame(c.rw)
	if err != nil {
		return f, err
	}
	c.FramesIn++
	c.BytesIn += int64(len(f.Payload))
	return f, nil
}

// Handler 处理一通电话，返回时连接关闭。
type Handler func(ctx context.Context, c *Conn) error

// Serve 在 addr 上监听 AudioSocket 连接，每通电话一个 goroutine。
func Serve(ctx context.Context, addr string, h Handler) error {
	// 主叫号码映射目录：拨号方案以 asterisk 身份写入，这里以 sipvoice 身份读取。
	// 目录不存在时通话仍能建立，只是会话/回拨会静默降级，所以只告警不阻断。
	if err := EnsureCallmapDir(); err != nil {
		log.Printf("[audiosock] 主叫号码映射目录不可用: %v（会话记忆与自动回拨会降级）", err)
	}
	go cleanCallmapLoop(ctx)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("audiosock: listen %s: %w", addr, err)
	}
	defer ln.Close()

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return err
			}
		}
		go serveConn(ctx, conn, h)
	}
}

func serveConn(ctx context.Context, conn net.Conn, h Handler) {
	defer conn.Close()

	// 首帧应是 UUID。
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	fr, err := ReadFrame(conn)
	if err != nil {
		log.Printf("[audiosock] %s: 读 UUID 失败: %v", conn.RemoteAddr(), err)
		return
	}
	if fr.Type != TypeUUID || len(fr.Payload) != 16 {
		log.Printf("[audiosock] %s: 首帧不是合法 UUID (type=0x%02x len=%d)", conn.RemoteAddr(), fr.Type, len(fr.Payload))
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	c := &Conn{UUID: FormatUUID(fr.Payload), Start: time.Now(), rw: conn}

	// 主叫号码：拨号方案在调用 AudioSocket 之前把 "uuid -> 号码" 写到 callmap 目录，
	// 这里按 UUID 读出来。读不到就用空（会话功能自动降级）。
	c.Caller = lookupCaller(c.UUID)

	log.Printf("[audiosock] 来电建立 uuid=%s caller=%q from=%s", c.UUID, c.Caller, conn.RemoteAddr())
	defer func() {
		log.Printf("[audiosock] 通话结束 uuid=%s 时长=%s 收=%d帧/%dB 发=%d帧/%dB",
			c.UUID, time.Since(c.Start).Round(time.Millisecond), c.FramesIn, c.BytesIn, c.FramesOut, c.BytesOut)
	}()

	if err := h(ctx, c); err != nil && err != io.EOF {
		log.Printf("[audiosock] uuid=%s handler: %v", c.UUID, err)
	}
}

// FormatUUID 把 16 字节二进制 UUID 转成标准字符串。
func FormatUUID(b []byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// CallmapDir 存放 uuid -> 主叫号码 的映射文件，由拨号方案写入。
const CallmapDir = "/var/lib/sipvoice/callmap"

// EnsureCallmapDir 建好映射目录，并确认它真的可写：拨号方案里的 System()
// 以 asterisk 身份写，agent 以 sipvoice 身份读。目录缺失时通话照旧，
// 只是主叫号码为空、会话与回拨静默降级。
func EnsureCallmapDir() error { return ensureWritableDir(CallmapDir) }

// ensureWritableDir 建目录并做一次写入探测。
func ensureWritableDir(dir string) error {
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return err
	}
	// 安装脚本可能已用 root 建好这个目录（0777），此时 chmod 会 EPERM，忽略即可；
	// 真正决定成败的是「能不能写」，所以用一次写入探测代替权限判断。
	_ = os.Chmod(dir, 0o777)
	f, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return nil
}

// cleanCallmapLoop 定期清掉没被消费的映射文件（例如呼叫没走到 AudioSocket 就挂了），
// 免得目录里越堆越多。取代早期版本依赖的 cron 清理。
func cleanCallmapLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			entries, err := os.ReadDir(CallmapDir)
			if err != nil {
				continue
			}
			cut := time.Now().Add(-10 * time.Minute)
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				info, err := e.Info()
				if err != nil || info.ModTime().After(cut) {
					continue
				}
				_ = os.Remove(filepath.Join(CallmapDir, e.Name()))
			}
		}
	}
}

// lookupCaller 按 UUID 读取主叫号码，读完即删。
func lookupCaller(uuid string) string {
	if uuid == "" {
		return ""
	}
	p := filepath.Join(CallmapDir, uuid)
	raw, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	_ = os.Remove(p)
	return strings.TrimSpace(string(raw))
}

// IsFlagged 检查并清除某次通话的特殊标记（如 AI 回拨的 "callback"）。
func IsFlagged(uuid string) bool {
	if uuid == "" {
		return false
	}
	p := filepath.Join(CallmapDir, uuid+".flag")
	if _, err := os.Stat(p); err != nil {
		return false
	}
	_ = os.Remove(p)
	return true
}
