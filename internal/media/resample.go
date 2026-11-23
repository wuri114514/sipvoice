// Package media 的音频重采样：在电话 8kHz 与 AI 服务常用 16k/24k 之间转换。
package media

import "math"

// Resample 把 inRate 的 16-bit 单声道 PCM 重采样为 outRate。
// 降采样时先做加窗 sinc 低通（抑制混叠），升采样用线性插值。
func Resample(in []int16, inRate, outRate int) []int16 {
	if inRate == outRate || len(in) == 0 {
		return in
	}
	src := in
	if outRate < inRate {
		src = lowPass(in, 0.45*float64(outRate)/float64(inRate))
	}
	n := len(in) * outRate / inRate
	out := make([]int16, n)
	ratio := float64(inRate) / float64(outRate)
	for i := 0; i < n; i++ {
		pos := float64(i) * ratio
		j := int(pos)
		frac := pos - float64(j)
		var a, b float64
		if j < len(src) {
			a = float64(src[j])
		}
		if j+1 < len(src) {
			b = float64(src[j+1])
		} else if j < len(src) {
			b = a
		}
		v := a + (b-a)*frac
		if v > math.MaxInt16 {
			v = math.MaxInt16
		} else if v < math.MinInt16 {
			v = math.MinInt16
		}
		out[i] = int16(v)
	}
	return out
}

// lowPass 用 31 抽头 Hamming 加窗 sinc 低通滤波，cutoff 为归一化截止频率（0~0.5）。
func lowPass(in []int16, cutoff float64) []int16 {
	if cutoff <= 0 || cutoff >= 0.5 {
		cp := make([]int16, len(in))
		copy(cp, in)
		return cp
	}
	const taps = 63
	const half = taps / 2
	h := make([]float64, taps)
	var sum float64
	for i := 0; i < taps; i++ {
		n := i - half
		var s float64
		if n == 0 {
			s = 2 * cutoff
		} else {
			s = math.Sin(2*math.Pi*cutoff*float64(n)) / (math.Pi * float64(n))
		}
		w := 0.54 - 0.46*math.Cos(2*math.Pi*float64(i)/float64(taps-1))
		h[i] = s * w
		sum += h[i]
	}
	// 归一化，保证直流增益为 1
	for i := range h {
		h[i] /= sum
	}

	out := make([]int16, len(in))
	for i := range in {
		var acc float64
		for k := 0; k < taps; k++ {
			j := i + k - half
			if j >= 0 && j < len(in) {
				acc += float64(in[j]) * h[k]
			}
		}
		if acc > math.MaxInt16 {
			acc = math.MaxInt16
		} else if acc < math.MinInt16 {
			acc = math.MinInt16
		}
		out[i] = int16(acc)
	}
	return out
}
