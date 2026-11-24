// Package config 负责加载/保存服务配置（JSON），供 agent 模式与 WebUI 共用。
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// Config 是全局配置。
type Config struct {
	Audio    AudioConfig    `json:"audio"`
	AI       AIConfig       `json:"ai"`
	Realtime RealtimeConfig `json:"realtime"`
	Agent    AgentConfig    `json:"agent"`
	Notify   NotifyConfig   `json:"notify"`
	Storage  StorageConfig  `json:"storage"`
}

// RealtimeConfig 描述端到端语音模型（Qwen-Omni-Realtime 等）。
// 启用后，通话不再走 ASR→LLM→TTS 三段式，而是直接把音频流喂给模型。
type RealtimeConfig struct {
	Enabled      bool   `json:"enabled"`
	BaseURL      string `json:"base_url"`
	APIKey       string `json:"api_key"`
	Model        string `json:"model"`
	Voice        string `json:"voice"`
	Instructions string `json:"instructions"`
}

// StorageConfig 描述录音与日志的保留策略（面板可改）。
type StorageConfig struct {
	RecordingEnabled bool   `json:"recording_enabled"`
	RecordingDir     string `json:"recording_dir"`
	QuotaGB          int    `json:"recording_quota_gb"`
	LogMaxMB         int    `json:"log_max_mb"`
	LogKeep          int    `json:"log_keep"`
}

// NotifyConfig 描述 ServerChan 推送。
type NotifyConfig struct {
	Enabled       bool   `json:"enabled"`
	ServerChanKey string `json:"serverchan_key"`
	MinIntervalMS int    `json:"min_interval_ms"`
	OnStartup     bool   `json:"on_startup"`
	OnError       bool   `json:"on_error"`
	OnDisk        bool   `json:"on_disk"`
	OnCallEnd     bool   `json:"on_call_end"`
}

// AudioConfig 描述 AudioSocket 监听。
type AudioConfig struct {
	Listen string `json:"listen"` // 如 127.0.0.1:9092
}

// AIConfig 描述 AI 服务（OpenAI 兼容）。
type AIConfig struct {
	// Provider 记录所选服务商预设（deepseek / siliconflow / openai…），仅供面板回显。
	Provider    string  `json:"provider"`
	BaseURL     string  `json:"base_url"`
	APIKey      string  `json:"api_key"`
	ChatModel   string  `json:"chat_model"`
	ASRModel    string  `json:"asr_model"`
	TTSModel    string  `json:"tts_model"`
	Voice       string  `json:"voice"`
	System      string  `json:"system_prompt"`
	Temperature float64 `json:"temperature"`
	MaxTokens   int     `json:"max_tokens"`

	// 本地后端：asr_backend = openai | whisper-cli | off
	//           tts_backend = openai | edge-tts | off
	ASRBackend   string `json:"asr_backend"`
	ASRProvider  string `json:"asr_provider"`
	ASRCmd       string `json:"asr_cmd"`
	ASRModelPath string `json:"asr_model_path"`
	ASRLang      string `json:"asr_lang"`
	ASRBaseURL   string `json:"asr_base_url"`
	ASRKey       string `json:"asr_api_key"`
	TTSBackend   string `json:"tts_backend"`
	TTSProvider  string `json:"tts_provider"`
	TTSCmd       string `json:"tts_cmd"`
	TTSVoice     string `json:"tts_voice"`
	TTSRate      string `json:"tts_rate"`
	TTSBaseURL   string `json:"tts_base_url"`
	TTSKey       string `json:"tts_api_key"`
}

// AgentConfig 描述对话行为。
type AgentConfig struct {
	Greeting            string     `json:"greeting"`
	MaxUtteranceSeconds int        `json:"max_utterance_seconds"`
	SilenceMS           int        `json:"silence_ms"`
	MinSpeechMS         int        `json:"min_speech_ms"`
	EnergyThreshold     float64    `json:"energy_threshold"`
	BargeIn             bool       `json:"barge_in"`
	MaxReplyChars       int        `json:"max_reply_chars"`
	ProcessingTone      ToneConfig `json:"processing_tone"`
	DTMF                DTMFConfig `json:"dtmf"`
}

// ToneConfig 描述"AI 处理中"提示音。
type ToneConfig struct {
	Enabled    bool    `json:"enabled"`
	Freq       float64 `json:"freq"`
	IntervalMS int     `json:"interval_ms"`
	DurationMS int     `json:"duration_ms"`
	Volume     float64 `json:"volume"`
}

// DTMFConfig 描述通话中按键控制。
type DTMFConfig struct {
	Enabled   bool   `json:"enabled"`
	Interrupt string `json:"interrupt"`
	Hangup    string `json:"hangup"`
}

var (
	mu      sync.RWMutex
	current *Config
	path    string
)

// Default 返回一份可直接运行的默认配置。
func Default() *Config {
	return &Config{
		Audio: AudioConfig{Listen: "127.0.0.1:9092"},
		AI: AIConfig{
			ChatModel:   "gpt-4o-mini",
			ASRModel:    "whisper-1",
			TTSModel:    "tts-1",
			Voice:       "alloy",
			System:      "你是一名电话语音助手。回答要简短、口语化，控制在两句话内，不要使用 Markdown。",
			Temperature: 0.7,
			ASRBackend:  "openai",
			TTSBackend:  "openai",
			TTSVoice:    "zh-CN-XiaoxiaoNeural",
		},
		Realtime: RealtimeConfig{
			Enabled:      false,
			BaseURL:      "wss://dashscope.aliyuncs.com/api-ws/v1/realtime",
			Model:        "qwen3.5-omni-flash-realtime",
			Voice:        "Tina",
			Instructions: "你是一名电话语音助手。回答简短、口语化，一般一两句话，不要使用 Markdown 或列表，不要念出标点符号。",
		},
		Notify: NotifyConfig{
			Enabled:       false,
			MinIntervalMS: 300000,
			OnStartup:     false,
			OnError:       true,
			OnDisk:        true,
			OnCallEnd:     false,
		},
		Storage: StorageConfig{
			RecordingEnabled: true,
			RecordingDir:     "/data/recordings",
			QuotaGB:          5,
			LogMaxMB:         50,
			LogKeep:          5,
		},
		Agent: AgentConfig{
			Greeting:            "您好，请问有什么可以帮您？",
			MaxUtteranceSeconds: 15,
			SilenceMS:           700,
			MinSpeechMS:         200,
			EnergyThreshold:     700,
			BargeIn:             true,
			MaxReplyChars:       0,
			ProcessingTone:      ToneConfig{Enabled: true, Freq: 800, IntervalMS: 1000, DurationMS: 120, Volume: 0.15},
			DTMF:                DTMFConfig{Enabled: true, Interrupt: "0", Hangup: "9"},
		},
	}
}

// Load 读取配置文件；文件不存在时返回默认配置。
func Load(p string) (*Config, error) {
	cfg := Default()
	if p == "" {
		mu.Lock()
		path = ""
		current = cfg
		mu.Unlock()
		return cfg, nil
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			mu.Lock()
			path = p
			current = cfg
			mu.Unlock()
			return cfg, nil
		}
		return nil, fmt.Errorf("config: %w", err)
	}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("config: 解析 %s: %w", p, err)
	}
	mu.Lock()
	path = p
	current = cfg
	mu.Unlock()
	return cfg, nil
}

// Get 返回当前配置（只读，调用方不应修改）。
func Get() *Config {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// Set 替换当前配置并写回文件。
func Set(c *Config) error {
	mu.Lock()
	defer mu.Unlock()
	current = c
	if path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

// Path 返回配置文件路径。
func Path() string {
	mu.RLock()
	defer mu.RUnlock()
	return path
}
