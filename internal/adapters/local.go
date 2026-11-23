// Package adapters 的本地后端：whisper.cpp（ASR）与 edge-tts（TTS）。
// 二者都以子进程方式调用 CLI，避免在 Go 里引入重型推理依赖。
package adapters

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"sipvoice/internal/media"
)

// WhisperCLI 用 whisper.cpp 的命令行工具做识别。
type WhisperCLI struct {
	Cmd   string // 例如 /usr/bin/whisper-cli
	Model string // ggml 模型路径
	Lang  string // zh / en / auto
}

// Transcribe 实现 pipeline.Transcriber。
func (w *WhisperCLI) Transcribe(ctx context.Context, wav []byte) (string, error) {
	if w.Cmd == "" {
		w.Cmd = "whisper-cli"
	}
	if w.Lang == "" {
		w.Lang = "zh"
	}
	dir, err := os.MkdirTemp("", "sipvoice-asr")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)

	// whisper.cpp 需要 16kHz；电话侧是 8kHz，先重采样。
	pcm, rate, err := media.DecodeWAV(wav)
	if err != nil {
		return "", fmt.Errorf("whisper-cli: 解析输入 WAV: %w", err)
	}
	if rate != 16000 {
		pcm = media.Resample(pcm, rate, 16000)
	}
	in := filepath.Join(dir, "in.wav")
	if err := os.WriteFile(in, media.EncodeWAV(pcm, 16000), 0o600); err != nil {
		return "", err
	}
	prefix := filepath.Join(dir, "out")

	args := []string{"-m", w.Model, "-f", in, "-l", w.Lang, "-otxt", "-of", prefix, "-nt", "-np"}
	// 中文场景加提示词，促使 whisper 输出简体而不是繁体。
	if strings.HasPrefix(w.Lang, "zh") {
		args = append(args, "--prompt", "以下是普通话的句子，请用简体中文输出。")
	}
	cmd := exec.CommandContext(ctx, w.Cmd, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("whisper-cli: %v: %s", err, truncate([]byte(stderr.String())))
	}
	raw, err := os.ReadFile(prefix + ".txt")
	if err != nil {
		return "", fmt.Errorf("whisper-cli: 读取结果: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

// EdgeTTS 用 edge-tts 命令行合成语音，再用 sox 转成 24k WAV。
type EdgeTTS struct {
	Cmd   string // 例如 edge-tts
	Voice string // 例如 zh-CN-XiaoxiaoNeural
	Rate  string // 例如 +0%
}

// Speak 实现 pipeline.Synthesizer，返回 WAV 字节。
func (e *EdgeTTS) Speak(ctx context.Context, text string) ([]byte, error) {
	if e.Cmd == "" {
		e.Cmd = "edge-tts"
	}
	if e.Voice == "" {
		e.Voice = "zh-CN-XiaoxiaoNeural"
	}
	dir, err := os.MkdirTemp("", "sipvoice-tts")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	mp3 := filepath.Join(dir, "out.mp3")
	wav := filepath.Join(dir, "out.wav")

	args := []string{"--voice", e.Voice, "--text", text, "--write-media", mp3}
	if e.Rate != "" {
		args = append(args, "--rate", e.Rate)
	}
	cmd := exec.CommandContext(ctx, e.Cmd, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.Stdout = &strings.Builder{}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("edge-tts: %v: %s", err, truncate([]byte(stderr.String())))
	}

	conv := exec.CommandContext(ctx, "sox", mp3, "-r", "24000", "-c", "1", "-b", "16", wav)
	if out, err := conv.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("sox 转码: %v: %s", err, truncate(out))
	}
	return os.ReadFile(wav)
}
