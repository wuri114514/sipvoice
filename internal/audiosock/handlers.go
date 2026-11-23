package audiosock

import (
	"context"
	"encoding/binary"
	"io"
	"log"
	"math"
	"os"
	"time"
)

// Echo 回声模式：把收到的音频原样回送，用于验证媒体往返链路。
func Echo(ctx context.Context, c *Conn) error {
	for {
		fr, err := ReadFrame(c.rw)
		if err != nil {
			return err
		}
		c.FramesIn++
		c.BytesIn += int64(len(fr.Payload))
		switch fr.Type {
		case TypeSlin8k:
			if err := c.WriteAudio(fr.Payload); err != nil {
				return err
			}
		case TypeHangup:
			return nil
		case TypeDTMF:
			log.Printf("[audiosock] uuid=%s DTMF %q", c.UUID, string(fr.Payload))
		}
	}
}

// Tone 持续播放 1kHz 正弦波（-12dBFS），用于验证下行链路。
func Tone(ctx context.Context, c *Conn) error {
	const freq = 1000.0
	const rate = 8000.0
	const amp = 0.25 * math.MaxInt16

	done := make(chan struct{})
	go func() { // 读线程：只处理挂断/DTMF
		defer close(done)
		for {
			fr, err := ReadFrame(c.rw)
			if err != nil {
				return
			}
			c.FramesIn++
			c.BytesIn += int64(len(fr.Payload))
			if fr.Type == TypeHangup {
				return
			}
		}
	}()

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	n := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-done:
			return nil
		case <-ticker.C:
			buf := make([]byte, FrameBytes)
			for i := 0; i < FrameBytes/2; i++ {
				v := int16(amp * math.Sin(2*math.Pi*freq*float64(n)/rate))
				binary.LittleEndian.PutUint16(buf[i*2:], uint16(v))
				n++
			}
			if err := c.WriteAudio(buf); err != nil {
				return err
			}
		}
	}
}

// WAV 把指定 8kHz/16-bit/mono WAV 文件循环播放，同时把上行音频录到 recPath。
func WAV(ctx context.Context, c *Conn, path, recPath string) error {
	pcm, err := readWAV(path)
	if err != nil {
		return err
	}
	var rec []byte
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			fr, err := ReadFrame(c.rw)
			if err != nil {
				return
			}
			c.FramesIn++
			c.BytesIn += int64(len(fr.Payload))
			switch fr.Type {
			case TypeSlin8k:
				rec = append(rec, fr.Payload...)
			case TypeHangup:
				return
			}
		}
	}()

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	off := 0
	for {
		select {
		case <-ctx.Done():
			writeWAV(recPath, rec)
			return nil
		case <-done:
			writeWAV(recPath, rec)
			return nil
		case <-ticker.C:
			if off >= len(pcm) {
				off = 0
			}
			end := off + FrameBytes
			if end > len(pcm) {
				end = len(pcm)
			}
			frame := make([]byte, FrameBytes)
			copy(frame, pcm[off:end])
			if err := c.WriteAudio(frame); err != nil {
				writeWAV(recPath, rec)
				return err
			}
			off = end
		}
	}
}

func readWAV(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	hdr := make([]byte, 44)
	if _, err := io.ReadFull(f, hdr); err != nil {
		return nil, err
	}
	if string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return nil, io.ErrUnexpectedEOF
	}
	return io.ReadAll(f)
}

func writeWAV(path string, pcm []byte) {
	if path == "" || len(pcm) == 0 {
		return
	}
	f, err := os.Create(path)
	if err != nil {
		log.Printf("[audiosock] 写录音失败: %v", err)
		return
	}
	defer f.Close()
	dataLen := uint32(len(pcm))
	hdr := make([]byte, 44)
	copy(hdr[0:], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], 36+dataLen)
	copy(hdr[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16)
	binary.LittleEndian.PutUint16(hdr[20:], 1)  // PCM
	binary.LittleEndian.PutUint16(hdr[22:], 1)  // mono
	binary.LittleEndian.PutUint32(hdr[24:], 8000)
	binary.LittleEndian.PutUint32(hdr[28:], 16000)
	binary.LittleEndian.PutUint16(hdr[32:], 2)
	binary.LittleEndian.PutUint16(hdr[34:], 16)
	copy(hdr[36:], "data")
	binary.LittleEndian.PutUint32(hdr[40:], dataLen)
	_, _ = f.Write(hdr)
	_, _ = f.Write(pcm)
	log.Printf("[audiosock] 录音写入 %s (%d 字节 PCM)", path, len(pcm))
}
