package media

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ConvertToWAV16k 调用 ffmpeg 把任意音频（wav / mp3 / m4a / ogg / webm / opus…）
// 转成 16kHz 单声道 16bit PCM WAV，供云端 ASR 与本地 whisper 使用。
func ConvertToWAV16k(ctx context.Context, data []byte, filename string) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("音频为空")
	}
	dir, err := os.MkdirTemp("", "sipvoice-conv")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" || len(ext) > 8 {
		ext = ".bin"
	}
	in := filepath.Join(dir, "in"+ext)
	out := filepath.Join(dir, "out.wav")
	if err := os.WriteFile(in, data, 0o600); err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", in,
		"-ac", "1", "-ar", "16000", "-acodec", "pcm_s16le",
		"-f", "wav", out,
	)
	if b, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ffmpeg 转码失败: %v: %s", err, trimErr(b))
	}
	return os.ReadFile(out)
}

// DurationSeconds 估算 16k 单声道 WAV 的时长（秒）；解析失败返回 0。
func DurationSeconds(wav []byte) float64 {
	pcm, rate, err := DecodeWAV(wav)
	if err != nil || rate <= 0 {
		return 0
	}
	return float64(len(pcm)) / float64(rate)
}

func trimErr(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}
