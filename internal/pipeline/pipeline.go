// Package pipeline 串联"上行语音 → ASR → AI → TTS → 下行语音"的对话回合。
package pipeline

import (
	"context"
	"fmt"
	"time"
)

// Transcriber 把一段语音转成文字。
type Transcriber interface {
	Transcribe(ctx context.Context, wav []byte) (string, error)
}

// Synthesizer 把文字合成为语音。
type Synthesizer interface {
	Speak(ctx context.Context, text string) ([]byte, error)
}

// ChatModel 是对话模型接口，便于后续接入流式/原生音频模型。
type ChatModel interface {
	Chat(ctx context.Context, system, user string) (string, error)
}

// Turn 记录一次对话回合的中间结果，便于日志与观测。
type Turn struct {
	StartedAt  time.Time
	Transcript string
	Reply      string
	AudioBytes int
	ASRLatency time.Duration
	AILatency  time.Duration
	TTSLatency time.Duration
}

// Bridge 编排单个对话回合。
type Bridge struct {
	ASR    Transcriber
	Chat   ChatModel
	TTS    Synthesizer
	System string // 系统提示词
}

// Result 是一次回合的完整产物。
type Result struct {
	Turn
	Audio []byte
}

// Handle 处理一段完整语音：识别 → 生成回复 → 合成语音。
func (b *Bridge) Handle(ctx context.Context, wav []byte) (*Result, error) {
	res := &Result{Turn: Turn{StartedAt: time.Now()}}

	t0 := time.Now()
	text, err := b.ASR.Transcribe(ctx, wav)
	if err != nil {
		return nil, fmt.Errorf("asr: %w", err)
	}
	res.Transcript = text
	res.ASRLatency = time.Since(t0)

	t1 := time.Now()
	reply, err := b.Chat.Chat(ctx, b.System, text)
	if err != nil {
		return nil, fmt.Errorf("chat: %w", err)
	}
	res.Reply = reply
	res.AILatency = time.Since(t1)

	t2 := time.Now()
	audio, err := b.TTS.Speak(ctx, reply)
	if err != nil {
		return nil, fmt.Errorf("tts: %w", err)
	}
	res.Audio = audio
	res.AudioBytes = len(audio)
	res.TTSLatency = time.Since(t2)
	return res, nil
}
