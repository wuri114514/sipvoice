// Package providers 汇总常见 AI 服务商的接入预设。
//
// 每家服务商给出：OpenAI 兼容的 Base URL、支持的能力（对话 / 识别 / 合成）、
// 常用模型名与音色。面板据此渲染“服务商”下拉框，切换时自动填好地址，
// 并可一键在线拉取该账号下真正可用的模型列表。
package providers

import "strings"

// Preset 描述一家服务商。
type Preset struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	BaseURL  string `json:"base_url"`
	Protocol string `json:"protocol"` // openai（默认） | ollama

	Chat bool `json:"chat"`
	ASR  bool `json:"asr"`
	TTS  bool `json:"tts"`

	ChatModels []string `json:"chat_models,omitempty"`
	ASRModels  []string `json:"asr_models,omitempty"`
	TTSModels  []string `json:"tts_models,omitempty"`
	Voices     []string `json:"voices,omitempty"`

	KeyURL string `json:"key_url,omitempty"`
	// ModelsURL 是该平台的模型选择/指引页面（面板上显示为"查看可用模型"链接）
	ModelsURL string `json:"models_url,omitempty"`
	Note      string `json:"note,omitempty"`
}

var presets = []Preset{
	{
		ID:      "deepseek",
		Name:    "深度求索 DeepSeek",
		BaseURL: "https://api.deepseek.com/v1",
		Chat:    true,
		ChatModels: []string{
			"deepseek-flash",
			"deepseek-v4-pro",
		},
		KeyURL:    "https://platform.deepseek.com/api_keys",
		ModelsURL: "https://platform.deepseek.com/api-docs/pricing",
		Note:      "仅提供对话模型；语音识别/合成需另配服务商。",
	},
	{
		ID:      "siliconflow",
		Name:    "硅基流动 SiliconFlow",
		BaseURL: "https://api.siliconflow.cn/v1",
		Chat:    true,
		ASR:     true,
		TTS:     true,
		ChatModels: []string{
			"deepseek-ai/DeepSeek-V3",
			"deepseek-ai/DeepSeek-R1",
			"Qwen/Qwen2.5-72B-Instruct",
			"Qwen/Qwen2.5-7B-Instruct",
		},
		ASRModels: []string{
			"FunAudioLLM/SenseVoiceSmall",
			"Qwen/Qwen3-ASR-1.7B",
		},
		TTSModels: []string{
			"FunAudioLLM/CosyVoice2-0.5B",
		},
		Voices: []string{
			"FunAudioLLM/CosyVoice2-0.5B:alex",
			"FunAudioLLM/CosyVoice2-0.5B:anna",
			"FunAudioLLM/CosyVoice2-0.5B:bella",
			"FunAudioLLM/CosyVoice2-0.5B:benjamin",
			"FunAudioLLM/CosyVoice2-0.5B:charles",
			"FunAudioLLM/CosyVoice2-0.5B:claire",
			"FunAudioLLM/CosyVoice2-0.5B:david",
			"FunAudioLLM/CosyVoice2-0.5B:diana",
		},
		KeyURL:    "https://cloud.siliconflow.cn/account/ak",
		ModelsURL: "https://cloud.siliconflow.cn/models",
	},
	{
		ID:      "dashscope",
		Name:    "阿里云百炼 DashScope（兼容模式）",
		BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
		Chat:    true,
		ChatModels: []string{
			"qwen-plus",
			"qwen-max",
			"qwen-turbo",
			"qwen-long",
		},
		KeyURL:    "https://bailian.console.aliyun.com/?apiKey=1",
		ModelsURL: "https://help.aliyun.com/zh/model-studio/models",
		Note:      "兼容模式只支持对话；语音识别/合成需走 DashScope 原生接口。",
	},
	{
		ID:         "openai",
		Name:       "OpenAI",
		BaseURL:    "https://api.openai.com/v1",
		Chat:       true,
		ASR:        true,
		TTS:        true,
		ChatModels: []string{"gpt-4o-mini", "gpt-4o", "gpt-4.1-mini"},
		ASRModels:  []string{"whisper-1", "gpt-4o-mini-transcribe"},
		TTSModels:  []string{"tts-1", "tts-1-hd", "gpt-4o-mini-tts"},
		Voices:     []string{"alloy", "echo", "fable", "onyx", "nova", "shimmer"},
		KeyURL:     "https://platform.openai.com/api-keys",
		ModelsURL:  "https://platform.openai.com/docs/models",
	},
	{
		ID:         "groq",
		Name:       "Groq（超快推理）",
		BaseURL:    "https://api.groq.com/openai/v1",
		Chat:       true,
		ASR:        true,
		ChatModels: []string{"llama-3.3-70b-versatile", "llama-3.1-8b-instant"},
		ASRModels:  []string{"whisper-large-v3-turbo", "whisper-large-v3"},
		KeyURL:     "https://console.groq.com/keys",
		ModelsURL:  "https://console.groq.com/docs/models",
		Note:       "识别速度极快、价格低，适合电话场景。",
	},
	{
		ID:         "zhipu",
		Name:       "智谱 GLM",
		BaseURL:    "https://open.bigmodel.cn/api/paas/v4",
		Chat:       true,
		ChatModels: []string{"glm-4-flash", "glm-4-plus", "glm-4-air"},
		KeyURL:     "https://bigmodel.cn/usercenter/apikeys",
		ModelsURL:  "https://open.bigmodel.cn/dev/howuse/model",
	},
	{
		ID:         "moonshot",
		Name:       "月之暗面 Kimi",
		BaseURL:    "https://api.moonshot.cn/v1",
		Chat:       true,
		ChatModels: []string{"moonshot-v1-8k", "moonshot-v1-32k", "kimi-latest"},
		KeyURL:     "https://platform.moonshot.cn/console/api-keys",
		ModelsURL:  "https://platform.moonshot.cn/docs/intro",
	},
	{
		ID:         "hunyuan",
		Name:       "腾讯混元",
		BaseURL:    "https://api.hunyuan.cloud.tencent.com/v1",
		Chat:       true,
		ChatModels: []string{"hunyuan-turbo", "hunyuan-large", "hunyuan-standard"},
		KeyURL:     "https://console.cloud.tencent.com/hunyuan/api-key",
		ModelsURL:  "https://cloud.tencent.com/document/product/1729/104753",
	},
	{
		ID:      "ark",
		Name:    "火山方舟 Ark",
		BaseURL: "https://ark.cn-beijing.volces.com/api/v3",
		Chat:    true,
		ChatModels: []string{
			"doubao-pro-32k",
			"doubao-lite-32k",
		},
		KeyURL:    "https://console.volcengine.com/ark",
		ModelsURL: "https://www.volcengine.com/docs/82379/1330310",
		Note:      "模型名也可填推理接入点 ID（ep-xxxxxxxx）。",
	},
	{
		ID:       "ollama",
		Name:     "本地 Ollama / vLLM",
		BaseURL:  "http://127.0.0.1:11434/v1",
		Protocol: "ollama",
		Chat:     true,
		ChatModels: []string{
			"qwen2.5:7b",
			"qwen2.5:3b",
			"llama3.1:8b",
		},
		Note: "本地部署时无需 Key；模型列表通过 Ollama 的 /api/tags 获取。",
	},
	{
		ID:   "custom",
		Name: "自定义（任意 OpenAI 兼容服务）",
		Chat: true,
		ASR:  true,
		TTS:  true,
		Note: "自行填写 Base URL；若地址只是域名，会自动补 /v1。",
	},
}

// All 返回全部预设（副本，调用方可安全修改）。
func All() []Preset {
	out := make([]Preset, len(presets))
	copy(out, presets)
	return out
}

// Get 按 ID 查找预设。
func Get(id string) (Preset, bool) {
	for _, p := range presets {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

// Guess 依据 Base URL 猜测服务商 ID，猜不出返回空串。
func Guess(base string) string {
	b := strings.ToLower(strings.TrimSpace(base))
	if b == "" {
		return ""
	}
	switch {
	case strings.Contains(b, "deepseek.com"):
		return "deepseek"
	case strings.Contains(b, "siliconflow"):
		return "siliconflow"
	case strings.Contains(b, "dashscope") || strings.Contains(b, "aliyuncs.com/compatible-mode"):
		return "dashscope"
	case strings.Contains(b, "openai.com"):
		return "openai"
	case strings.Contains(b, "groq.com"):
		return "groq"
	case strings.Contains(b, "bigmodel.cn"):
		return "zhipu"
	case strings.Contains(b, "moonshot.cn"):
		return "moonshot"
	case strings.Contains(b, "hunyuan"):
		return "hunyuan"
	case strings.Contains(b, "volces.com"):
		return "ark"
	case strings.Contains(b, "11434") || strings.Contains(b, "ollama"):
		return "ollama"
	}
	return ""
}
