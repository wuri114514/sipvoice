package media

import (
	"math"
	"testing"
)

func TestResampleDownsampleLength(t *testing.T) {
	in := make([]int16, 24000)
	got := Resample(in, 24000, 8000)
	if len(got) != 8000 {
		t.Fatalf("len = %d, want 8000", len(got))
	}
}

func TestResampleUpsampleLength(t *testing.T) {
	in := make([]int16, 8000)
	got := Resample(in, 8000, 16000)
	if len(got) != 16000 {
		t.Fatalf("len = %d, want 16000", len(got))
	}
}

// 1kHz 正弦降采样后，每秒过零次数应约为 2000（两个过零/周期）。
func TestResampleDownsamplePreservesFrequency(t *testing.T) {
	const inRate = 24000
	const outRate = 8000
	const freq = 1000.0
	in := make([]int16, inRate) // 1 秒
	for i := range in {
		in[i] = int16(8000 * math.Sin(2*math.Pi*freq*float64(i)/inRate))
	}
	out := Resample(in, inRate, outRate)

	zeros := 0
	for i := 1; i < len(out); i++ {
		if (out[i-1] < 0 && out[i] >= 0) || (out[i-1] >= 0 && out[i] < 0) {
			zeros++
		}
	}
	// 期望 ~2000 次，留 10% 容差
	if zeros < 1800 || zeros > 2200 {
		t.Fatalf("zero crossings = %d, want ~2000", zeros)
	}
}

func TestResampleSameRateReturnsInput(t *testing.T) {
	in := []int16{1, -2, 3, -4}
	got := Resample(in, 8000, 8000)
	if len(got) != len(in) {
		t.Fatalf("len = %d, want %d", len(got), len(in))
	}
	if &got[0] != &in[0] {
		t.Fatal("same-rate should return the same slice")
	}
}

// 低通应把 5kHz 分量（高于 8k 的奈奎斯特前 0.45 截止）显著衰减。
func TestLowPassAttenuatesHighFrequency(t *testing.T) {
	const inRate = 24000
	const freq = 5000.0
	in := make([]int16, inRate)
	for i := range in {
		in[i] = int16(8000 * math.Sin(2*math.Pi*freq*float64(i)/inRate))
	}
	out := Resample(in, inRate, 8000)
	var peak float64
	for _, v := range out[100:] {
		if a := math.Abs(float64(v)); a > peak {
			peak = a
		}
	}
	if peak > 8000*0.35 {
		t.Fatalf("5kHz peak after low-pass = %.0f, want much smaller than 8000", peak)
	}
}
