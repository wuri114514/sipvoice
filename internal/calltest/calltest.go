// Package calltest 是一个极简 SIP UAC + RTP 收发器，用于自动化端到端验证：
// 发起 INVITE → 收到 200 OK → ACK → 双向 RTP（G.711 µ-law）→ BYE。
// 它不替代真实话机，只作为部署自检（-mode calltest）。
package calltest

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"sipvoice/internal/media"
	"sipvoice/internal/sip"
)

// Config 是 calltest 的入参。
type Config struct {
	Target   string        // Asterisk 地址，如 127.0.0.1:5060
	Number   string        // 被叫号码，如 9000
	WAVIn    string        // 可选：作为上行音频循环播放的 8k/16bit/mono WAV
	WAVOut   string        // 可选：下行录音输出路径
	Duration time.Duration // 通话时长
}

// Run 执行一次完整呼叫测试。
func Run(ctx context.Context, cfg Config) error {
	if cfg.Target == "" {
		cfg.Target = "127.0.0.1:5060"
	}
	if cfg.Number == "" {
		cfg.Number = "9000"
	}
	if cfg.Duration <= 0 {
		cfg.Duration = 5 * time.Second
	}

	raddr, err := net.ResolveUDPAddr("udp", cfg.Target)
	if err != nil {
		return fmt.Errorf("calltest: resolve target: %w", err)
	}
	sipConn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return fmt.Errorf("calltest: sip dial: %w", err)
	}
	defer sipConn.Close()

	localIP := sipConn.LocalAddr().(*net.UDPAddr).IP.String()
	rtpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return fmt.Errorf("calltest: rtp listen: %w", err)
	}
	defer rtpConn.Close()
	rtpPort := rtpConn.LocalAddr().(*net.UDPAddr).Port

	callID := fmt.Sprintf("%x@calltest", rand.Uint64())
	fromTag := fmt.Sprintf("%x", rand.Uint64())
	branch := fmt.Sprintf("z9hG4bK%x", rand.Uint64())
	cseq := 1

	sdp := fmt.Sprintf("v=0\r\no=- %d 1 IN IP4 %s\r\ns=calltest\r\nc=IN IP4 %s\r\nt=0 0\r\n"+
		"m=audio %d RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\na=sendrecv\r\n",
		time.Now().Unix(), localIP, localIP, rtpPort)

	req := strings.Join([]string{
		fmt.Sprintf("INVITE sip:%s@%s SIP/2.0", cfg.Number, cfg.Target),
		"Via: SIP/2.0/UDP " + localIP + ":" + strconv.Itoa(sipConn.LocalAddr().(*net.UDPAddr).Port) + ";branch=" + branch,
		"Max-Forwards: 70",
		fmt.Sprintf("From: <sip:calltest@%s>;tag=%s", localIP, fromTag),
		fmt.Sprintf("To: <sip:%s@%s>", cfg.Number, cfg.Target),
		"Call-ID: " + callID,
		fmt.Sprintf("CSeq: %d INVITE", cseq),
		fmt.Sprintf("Contact: <sip:calltest@%s:%d>", localIP, sipConn.LocalAddr().(*net.UDPAddr).Port),
		"Content-Type: application/sdp",
		"Content-Length: " + strconv.Itoa(len(sdp)),
		"", sdp,
	}, "\r\n")

	if _, err := sipConn.Write([]byte(req)); err != nil {
		return fmt.Errorf("calltest: send INVITE: %w", err)
	}
	fmt.Printf("[calltest] INVITE -> %s (%s), RTP %d\n", cfg.Target, cfg.Number, rtpPort)

	deadline := time.Now().Add(15 * time.Second)
	_ = sipConn.SetReadDeadline(deadline)

	buf := make([]byte, 8192)
	var ok *sip.Message
	for time.Now().Before(deadline) {
		n, err := sipConn.Read(buf)
		if err != nil {
			return fmt.Errorf("calltest: 等待应答失败: %w", err)
		}
		m, err := sip.Parse(buf[:n])
		if err != nil {
			continue
		}
		if m.StatusCode == 0 {
			continue
		}
		fmt.Printf("[calltest] <- %s\n", m.StartLine)
		if m.StatusCode == 401 || m.StatusCode == 403 {
			return fmt.Errorf("calltest: 呼叫被拒（%d），检查 endpoint/identify 配置", m.StatusCode)
		}
		if m.StatusCode >= 200 {
			ok = m
			break
		}
	}
	if ok == nil {
		return fmt.Errorf("calltest: 15 秒内未收到 200 OK")
	}

	remoteIP, remotePort, err := parseSDP(ok.Body)
	if err != nil {
		return fmt.Errorf("calltest: 解析应答 SDP: %w", err)
	}
	fmt.Printf("[calltest] 200 OK, 对端 RTP %s:%d\n", remoteIP, remotePort)

	// ACK
	toHdr := ok.Get("to")
	ack := strings.Join([]string{
		fmt.Sprintf("ACK sip:%s@%s SIP/2.0", cfg.Number, cfg.Target),
		"Via: SIP/2.0/UDP " + localIP + ":" + strconv.Itoa(sipConn.LocalAddr().(*net.UDPAddr).Port) + ";branch=" + branch,
		"Max-Forwards: 70",
		fmt.Sprintf("From: <sip:calltest@%s>;tag=%s", localIP, fromTag),
		"To: " + toHdr,
		"Call-ID: " + callID,
		fmt.Sprintf("CSeq: %d ACK", cseq),
		"Content-Length: 0",
		"", "",
	}, "\r\n")
	if _, err := sipConn.Write([]byte(ack)); err != nil {
		return fmt.Errorf("calltest: send ACK: %w", err)
	}

	// RTP 双向
	stop := make(chan struct{})
	recCh := make(chan []byte, 1)
	go recvRTP(rtpConn, stop, recCh)

	var pcmIn []byte
	if cfg.WAVIn != "" {
		if b, rerr := readWAV(cfg.WAVIn); rerr == nil {
			pcmIn = b
		} else {
			fmt.Printf("[calltest] 播放文件不可用（%v），改为静音\n", rerr)
		}
	}
	sendDone := make(chan struct{})
	go sendRTP(rtpConn, remoteIP, remotePort, pcmIn, stop, sendDone)

	select {
	case <-ctx.Done():
	case <-time.After(cfg.Duration):
	}
	close(stop)
	<-sendDone

	var rec []byte
	select {
	case rec = <-recCh:
	case <-time.After(800 * time.Millisecond):
	}

	// BYE
	byeBranch := fmt.Sprintf("z9hG4bK%x", rand.Uint64())
	bye := strings.Join([]string{
		fmt.Sprintf("BYE sip:%s@%s SIP/2.0", cfg.Number, cfg.Target),
		"Via: SIP/2.0/UDP " + localIP + ":" + strconv.Itoa(sipConn.LocalAddr().(*net.UDPAddr).Port) + ";branch=" + byeBranch,
		"Max-Forwards: 70",
		fmt.Sprintf("From: <sip:calltest@%s>;tag=%s", localIP, fromTag),
		"To: " + toHdr,
		"Call-ID: " + callID,
		fmt.Sprintf("CSeq: %d BYE", cseq+1),
		"Content-Length: 0",
		"", "",
	}, "\r\n")
	_, _ = sipConn.Write([]byte(bye))
	_ = sipConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if n, err := sipConn.Read(buf); err == nil {
		if m, perr := sip.Parse(buf[:n]); perr == nil {
			fmt.Printf("[calltest] <- %s\n", m.StartLine)
		}
	}

	if cfg.WAVOut != "" && len(rec) > 0 {
		if err := writeWAV(cfg.WAVOut, rec); err != nil {
			return fmt.Errorf("calltest: 写录音: %w", err)
		}
		fmt.Printf("[calltest] 录音写入 %s (%d 字节 PCM)\n", cfg.WAVOut, len(rec))
	}
	return nil
}

func recvRTP(conn *net.UDPConn, stop <-chan struct{}, out chan<- []byte) {
	var pcm []byte
	buf := make([]byte, 2000)
	for {
		select {
		case <-stop:
			out <- pcm
			return
		default:
		}
		_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		h, err := media.ParseHeader(buf[:n])
		if err != nil {
			continue
		}
		payload, err := h.Payload(buf[:n])
		if err != nil {
			continue
		}
		lin := media.PCMU2Linear(payload)
		pcm = append(pcm, int16sToBytes(lin)...)
	}
}

func sendRTP(conn *net.UDPConn, ip string, port int, pcm []byte, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	dst := &net.UDPAddr{IP: net.ParseIP(ip), Port: port}
	if dst.IP == nil {
		fmt.Printf("[calltest] 对端 IP 无效: %s\n", ip)
		return
	}
	var seq uint16
	var ts uint32
	ssrc := rand.Uint32()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	off := 0
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			samples := make([]int16, 160)
			if len(pcm) > 0 {
				for i := 0; i < 160; i++ {
					if off+1 < len(pcm) {
						samples[i] = int16(binary.LittleEndian.Uint16(pcm[off:]))
						off += 2
					} else {
						off = 0
						samples[i] = 0
					}
				}
			}
			h := media.Header{Version: 2, PayloadType: 0, SequenceNumber: seq, Timestamp: ts, SSRC: ssrc}
			pkt := append(h.Marshal(), media.Linear2PCMU(samples)...)
			_, _ = conn.WriteToUDP(pkt, dst)
			seq++
			ts += 160
		}
	}
}

func int16sToBytes(in []int16) []byte {
	out := make([]byte, len(in)*2)
	for i, v := range in {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(v))
	}
	return out
}

func parseSDP(body []byte) (string, int, error) {
	var ip string
	var port int
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "c=IN IP4 "):
			ip = strings.TrimSpace(strings.TrimPrefix(line, "c=IN IP4 "))
		case strings.HasPrefix(line, "m=audio "):
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				port, _ = strconv.Atoi(fields[1])
			}
		}
	}
	if ip == "" || port == 0 {
		return "", 0, fmt.Errorf("SDP 缺少 c= 或 m=audio: %q", string(body))
	}
	return ip, port, nil
}

func readWAV(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(44, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}

func writeWAV(path string, pcm []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	n := uint32(len(pcm))
	hdr := make([]byte, 44)
	copy(hdr, "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], 36+n)
	copy(hdr[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16)
	binary.LittleEndian.PutUint16(hdr[20:], 1)
	binary.LittleEndian.PutUint16(hdr[22:], 1)
	binary.LittleEndian.PutUint32(hdr[24:], 8000)
	binary.LittleEndian.PutUint32(hdr[28:], 16000)
	binary.LittleEndian.PutUint16(hdr[32:], 2)
	binary.LittleEndian.PutUint16(hdr[34:], 16)
	copy(hdr[36:], "data")
	binary.LittleEndian.PutUint32(hdr[40:], n)
	if _, err := f.Write(hdr); err != nil {
		return err
	}
	_, err = f.Write(pcm)
	return err
}
