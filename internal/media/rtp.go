// Package media 提供最小可用的 RTP 头处理与 G.711 编解码，作为语音链路的底座。
// 后续接入 Pion 时可直接替换本包，接口保持不变。
package media

import (
	"encoding/binary"
	"fmt"
)

// Header 是 RTP 固定头（12 字节）+ 可选 CSRC 的简化视图。
type Header struct {
	Version        uint8
	Padding        bool
	Extension      bool
	CSRCCount      uint8
	Marker         bool
	PayloadType    uint8
	SequenceNumber uint16
	Timestamp      uint32
	SSRC           uint32
}

// Marshal 把头部编码为 12 字节。
func (h Header) Marshal() []byte {
	b := make([]byte, 12)
	b[0] = (h.Version&0x03)<<6 | boolBit(h.Padding)<<5 | boolBit(h.Extension)<<4 | (h.CSRCCount & 0x0F)
	b[1] = boolBit(h.Marker)<<7 | (h.PayloadType & 0x7F)
	binary.BigEndian.PutUint16(b[2:], h.SequenceNumber)
	binary.BigEndian.PutUint32(b[4:], h.Timestamp)
	binary.BigEndian.PutUint32(b[8:], h.SSRC)
	return b
}

// ParseHeader 解析 12 字节 RTP 固定头。
func ParseHeader(b []byte) (Header, error) {
	if len(b) < 12 {
		return Header{}, fmt.Errorf("rtp: header too short: %d", len(b))
	}
	h := Header{
		Version:        b[0] >> 6,
		Padding:        b[0]&0x20 != 0,
		Extension:      b[0]&0x10 != 0,
		CSRCCount:      b[0] & 0x0F,
		Marker:         b[1]&0x80 != 0,
		PayloadType:    b[1] & 0x7F,
		SequenceNumber: binary.BigEndian.Uint16(b[2:]),
		Timestamp:      binary.BigEndian.Uint32(b[4:]),
		SSRC:           binary.BigEndian.Uint32(b[8:]),
	}
	if h.Version != 2 {
		return Header{}, fmt.Errorf("rtp: unsupported version %d", h.Version)
	}
	return h, nil
}

// Payload 返回去掉头部与 CSRC 后的负载。
func (h Header) Payload(pkt []byte) ([]byte, error) {
	off := 12 + int(h.CSRCCount)*4
	if len(pkt) < off {
		return nil, fmt.Errorf("rtp: packet shorter than header")
	}
	return pkt[off:], nil
}

func boolBit(v bool) byte {
	if v {
		return 1
	}
	return 0
}

// PCMU2Linear 将 8kHz G.711 µ-law 解码为 16-bit 线性 PCM。
func PCMU2Linear(in []byte) []int16 {
	out := make([]int16, len(in))
	for i, u := range in {
		out[i] = mulaw2linear(u)
	}
	return out
}

// Linear2PCMU 将 16-bit 线性 PCM 编码为 G.711 µ-law。
func Linear2PCMU(in []int16) []byte {
	out := make([]byte, len(in))
	for i, s := range in {
		out[i] = linear2mulaw(s)
	}
	return out
}

func mulaw2linear(u byte) int16 {
	u = ^u
	sign := u & 0x80
	exponent := (u >> 4) & 0x07
	mantissa := u & 0x0F
	sample := int16((int(mantissa)<<3 + 0x84) << exponent)
	sample -= 0x84
	if sign != 0 {
		return -sample
	}
	return sample
}

func linear2mulaw(s int16) byte {
	const bias = 0x84
	const clip = 32635
	sign := byte(0)
	if s < 0 {
		sign = 0x80
		s = -s
	}
	if s > clip {
		s = clip
	}
	s += bias
	exponent := byte(7)
	mask := int16(0x4000)
	for exponent > 0 && s&mask == 0 {
		exponent--
		mask >>= 1
	}
	mantissa := byte((int(s) >> (exponent + 3)) & 0x0F)
	return ^(sign | exponent<<4 | mantissa)
}
