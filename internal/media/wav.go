// Package media 的 WAV 编解码（16-bit 单声道 PCM）。
package media

import (
	"encoding/binary"
	"fmt"
	"math"
)

// EncodeWAV 把 PCM 打包成 WAV 字节。
func EncodeWAV(pcm []int16, rate int) []byte {
	n := len(pcm) * 2
	out := make([]byte, 44+n)
	copy(out[0:], "RIFF")
	binary.LittleEndian.PutUint32(out[4:], uint32(36+n))
	copy(out[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(out[16:], 16)
	binary.LittleEndian.PutUint16(out[20:], 1) // PCM
	binary.LittleEndian.PutUint16(out[22:], 1) // mono
	binary.LittleEndian.PutUint32(out[24:], uint32(rate))
	binary.LittleEndian.PutUint32(out[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(out[32:], 2)
	binary.LittleEndian.PutUint16(out[34:], 16)
	copy(out[36:], "data")
	binary.LittleEndian.PutUint32(out[40:], uint32(n))
	for i, v := range pcm {
		binary.LittleEndian.PutUint16(out[44+i*2:], uint16(v))
	}
	return out
}

// DecodeWAV 解析 16-bit 单声道 WAV，返回 PCM 与采样率。
// 支持带附加 chunk 的 WAV（按 chunk 遍历，不假定 44 字节头）。
func DecodeWAV(b []byte) ([]int16, int, error) {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, 0, fmt.Errorf("wav: 不是 RIFF/WAVE")
	}
	var (
		rate     int
		channels int = 1
		bits     int = 16
		data     []byte
	)
	i := 12
	for i+8 <= len(b) {
		id := string(b[i : i+4])
		size := int(binary.LittleEndian.Uint32(b[i+4 : i+8]))
		body := i + 8
		if body+size > len(b) {
			size = len(b) - body
		}
		switch id {
		case "fmt ":
			if size >= 16 {
				format := binary.LittleEndian.Uint16(b[body:])
				channels = int(binary.LittleEndian.Uint16(b[body+2:]))
				rate = int(binary.LittleEndian.Uint32(b[body+4:]))
				bits = int(binary.LittleEndian.Uint16(b[body+14:]))
				if format != 1 {
					return nil, 0, fmt.Errorf("wav: 不支持的格式码 %d", format)
				}
			}
		case "data":
			data = b[body : body+size]
		}
		i = body + size
		if size%2 == 1 {
			i++
		}
	}
	if rate == 0 || data == nil {
		return nil, 0, fmt.Errorf("wav: 缺少 fmt 或 data chunk")
	}
	if bits != 16 {
		return nil, 0, fmt.Errorf("wav: 只支持 16-bit，当前 %d", bits)
	}
	n := len(data) / 2 / channels
	pcm := make([]int16, n)
	for k := 0; k < n; k++ {
		pcm[k] = int16(binary.LittleEndian.Uint16(data[k*2*channels:]))
	}
	return pcm, rate, nil
}

// RMS 返回一段 PCM 的均方根幅度。
func RMS(pcm []int16) float64 {
	if len(pcm) == 0 {
		return 0
	}
	var sum float64
	for _, v := range pcm {
		f := float64(v)
		sum += f * f
	}
	return math.Sqrt(sum / float64(len(pcm)))
}
