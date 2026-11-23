// Package aibuild 依据配置创建 ASR / 对话 / TTS 适配器。
// 通话主流程与面板的“在线测试”共用这里的构建逻辑，确保测出来的就是实际用的。
package aibuild

import (
	"os"
	"strings"

	"sipvoice/internal/adapters"
	"sipvoice/internal/config"
	"sipvoice/internal/pipeline"
)

// Providers 是一套构建好的 AI 适配器；未启用的能力为 nil。
type Providers struct {
	ASR  pipeline.Transcriber
	Chat pipeline.ChatModel
	TTS  pipeline.Synthesizer
}

// Build 按配置构建全部适配器。
func Build(c *config.Config) Providers {
	return Providers{
		ASR:  BuildASR(c),
		Chat: BuildChat(c),
		TTS:  BuildTTS(c, ""),
	}
}

// BuildASR 构建语音识别适配器。
func BuildASR(c *config.Config) pipeline.Transcriber {
	ai := c.AI
	switch normalize(ai.ASRBackend) {
	case "off":
		return nil
	case "whisper-cli", "local":
		cmd := ai.ASRCmd
		if cmd == "" {
			cmd = "whisper-cli"
		}
		return &adapters.WhisperCLI{Cmd: cmd, Model: ai.ASRModelPath, Lang: ai.ASRLang}
	case "siliconflow":
		base := first(ai.ASRBaseURL, "https://api.siliconflow.cn/v1")
		key := first(ai.ASRKey, os.Getenv("SILICONFLOW_API_KEY"), ai.APIKey)
		model := first(ai.ASRModel, "FunAudioLLM/SenseVoiceSmall")
		return adapters.New(base, key, model)
	default: // openai 兼容
		base := first(ai.ASRBaseURL, ai.BaseURL)
		if base == "" {
			return nil
		}
		return adapters.New(base, first(ai.ASRKey, ai.APIKey), first(ai.ASRModel, "whisper-1"))
	}
}

// BuildChat 构建对话模型适配器。
func BuildChat(c *config.Config) pipeline.ChatModel {
	ai := c.AI
	if strings.TrimSpace(ai.BaseURL) == "" {
		return nil
	}
	key := first(ai.APIKey, os.Getenv("SIPVOICE_API_KEY"))
	cc := adapters.New(ai.BaseURL, key, ai.ChatModel)
	cc.MaxTokens = ai.MaxTokens
	return cc
}

// BuildTTS 构建语音合成适配器；voice 非空时覆盖配置中的音色（用于试听）。
func BuildTTS(c *config.Config, voice string) pipeline.Synthesizer {
	ai := c.AI
	switch normalize(ai.TTSBackend) {
	case "off":
		return nil
	case "edge-tts":
		cmd := ai.TTSCmd
		if cmd == "" {
			cmd = "edge-tts"
		}
		return &adapters.EdgeTTS{Cmd: cmd, Voice: first(voice, ai.TTSVoice, "zh-CN-XiaoxiaoNeural"), Rate: ai.TTSRate}
	case "siliconflow":
		base := first(ai.TTSBaseURL, "https://api.siliconflow.cn/v1")
		key := first(ai.TTSKey, os.Getenv("SILICONFLOW_API_KEY"), ai.APIKey)
		model := first(ai.TTSModel, "FunAudioLLM/CosyVoice2-0.5B")
		v := first(voice, ai.TTSVoice, model+":alex")
		return &adapters.Speech{C: adapters.New(base, key, model), Voice: v}
	default: // openai 兼容
		base := first(ai.TTSBaseURL, ai.BaseURL)
		if base == "" {
			return nil
		}
		key := first(ai.TTSKey, ai.APIKey)
		model := first(ai.TTSModel, "tts-1")
		return &adapters.Speech{C: adapters.New(base, key, model), Voice: first(voice, ai.Voice, "alloy")}
	}
}

// ASRBackendName / TTSBackendName 返回供界面展示的后端名。
func ASRBackendName(c *config.Config) string {
	name := normalize(c.AI.ASRBackend)
	if name == "" {
		return "openai"
	}
	return name
}

// TTSBackendName 返回供界面展示的后端名。
func TTSBackendName(c *config.Config) string {
	name := normalize(c.AI.TTSBackend)
	if name == "" {
		return "openai"
	}
	return name
}

func normalize(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

func first(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
