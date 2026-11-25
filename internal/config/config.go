// Package config 负责加载/保存服务配置（JSON），供 agent 模式与 WebUI 共用。
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	Session  SessionConfig  `json:"session"`
}

// SessionConfig 描述异步会话（同一号码保留对话上下文）。
type SessionConfig struct {
	Enabled        bool   `json:"enabled"`         // 是否启用会话记忆
	TTLMinutes     int    `json:"ttl_minutes"`     // 挂断后保留多久（分钟）
	MaxMessages    int    `json:"max_messages"`    // 最多保留多少条历史
	CallbackPolicy string `json:"callback_policy"` // 回拨策略：ask（先问）/ notify（先通知）/ never
	// CallbackDelaySec 是传真分析完成后等待多久再回拨（给用户走到电话旁的时间）
	CallbackDelaySec int `json:"callback_delay_sec"`
	// AutoCallbackOnFax 传真分析完成后是否自动回拨告知
	AutoCallbackOnFax bool `json:"auto_callback_on_fax"`
	// CallbackGreeting 回拨接通后播放的开场白（可改，TTS 一次性预生成）
	CallbackGreeting string `json:"callback_greeting"`
	// CallbackGreetingFile 预生成的回拨开场白音频
	CallbackGreetingFile string `json:"callback_greeting_file"`
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

// NotifyConfig 描述通知推送（ServerChan + 邮局两条通道）。
type NotifyConfig struct {
	Enabled       bool   `json:"enabled"`
	ServerChanKey string `json:"serverchan_key"`
	MinIntervalMS int    `json:"min_interval_ms"`
	OnStartup     bool   `json:"on_startup"`
	OnError       bool   `json:"on_error"`
	OnDisk        bool   `json:"on_disk"`
	OnCallEnd     bool   `json:"on_call_end"`

	// 邮局通道：与 ServerChan 并存，可各自独立开关
	MailEnabled bool   `json:"mail_enabled"`
	MailSubject string `json:"mail_subject"`
	MailBody    string `json:"mail_body"`
}

// AudioConfig 描述 AudioSocket 监听。
type AudioConfig struct {
	Listen string `json:"listen"` // 如 127.0.0.1:9092
}

// AIConfig 描述 AI 服务（OpenAI 兼容）。
type AIConfig struct {
	// Provider 记录所选服务商预设（deepseek / siliconflow / openai…），仅供面板回显。
	Provider  string `json:"provider"`
	BaseURL   string `json:"base_url"`
	APIKey    string `json:"api_key"`
	ChatModel string `json:"chat_model"`
	ASRModel  string `json:"asr_model"`
	TTSModel  string `json:"tts_model"`
	// VisionEnabled 当前对话模型是否支持视觉（决定传真联动等入口是否显示）
	VisionEnabled bool    `json:"vision_enabled"`
	Voice         string  `json:"voice"`
	System        string  `json:"system_prompt"`
	Temperature   float64 `json:"temperature"`
	MaxTokens     int     `json:"max_tokens"`

	// ProviderKeys 保存各服务商各自的 API Key，切换服务商时自动带出，
	// 避免"切到别家却还在用上一家的 key"导致 401。
	ProviderKeys map[string]string `json:"provider_keys,omitempty"`

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
	Greeting            string  `json:"greeting"`
	MaxUtteranceSeconds int     `json:"max_utterance_seconds"`
	SilenceMS           int     `json:"silence_ms"`
	MinSpeechMS         int     `json:"min_speech_ms"`
	EnergyThreshold     float64 `json:"energy_threshold"`
	BargeIn             bool    `json:"barge_in"`
	MaxReplyChars       int     `json:"max_reply_chars"`
	// PreRollAfterPlayMS 是问候语/播报结束后继续采集的毫秒数，
	// 用于避免用户抢话时开头几个字被 ASR 丢掉（默认 1000）
	PreRollAfterPlayMS int `json:"pre_roll_after_play_ms"`
	// BeepPrompt 开场白之后是否播「请在滴声之后开始说话」再滴一声（免提场景对齐时间点）
	BeepPrompt bool `json:"beep_prompt"`
	// BeepPromptFile 滴声提示音频（预生成）
	BeepPromptFile string `json:"beep_prompt_file"`
	// BeepMuteMS 滴声之后的 VAD 屏蔽毫秒数，吃掉扬声器余响（默认 300）
	BeepMuteMS int `json:"beep_mute_ms"`
	// PlayBufferMS 播放前的预灌缓冲毫秒数，防止软电话抖动缓冲断流（默认 300）
	PlayBufferMS   int        `json:"play_buffer_ms"`
	ProcessingTone ToneConfig `json:"processing_tone"`
	DTMF           DTMFConfig `json:"dtmf"`
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
			ChatModel:     "gpt-4o-mini",
			ASRModel:      "whisper-1",
			TTSModel:      "tts-1",
			VisionEnabled: false,
			Voice:         "alloy",
			System:        "你是一名电话语音助手。回答要简短、口语化，控制在两句话内，不要使用 Markdown。",
			Temperature:   0.7,
			ASRBackend:    "openai",
			TTSBackend:    "openai",
			TTSVoice:      "zh-CN-XiaoxiaoNeural",
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
			MailEnabled:   false,
			MailSubject:   "[SIPVoice] {title}",
			MailBody:      "{title}\r\n\r\n{body}\r\n\r\n时间：{time}\r\n主机：{host}",
			OnStartup:     false,
			OnError:       true,
			OnDisk:        true,
			OnCallEnd:     false,
		},
		Session: SessionConfig{
			Enabled:              true,
			TTLMinutes:           10,
			MaxMessages:          20,
			CallbackPolicy:       "ask",
			CallbackDelaySec:     10,
			AutoCallbackOnFax:    false,
			CallbackGreeting:     "传真已被接收并分析，让我们继续。",
			CallbackGreetingFile: "/var/lib/sipvoice/prompts/callback-intro.wav",
		},
		Storage: StorageConfig{
			RecordingEnabled: true,
			RecordingDir:     "/data/recordings",
			QuotaGB:          5,
			LogMaxMB:         50,
			LogKeep:          5,
		},
		Agent: AgentConfig{
			Greeting:            "你好，我是 AI 语音助手，请在滴声之后说话。",
			MaxUtteranceSeconds: 15,
			SilenceMS:           700,
			MinSpeechMS:         200,
			EnergyThreshold:     700,
			BargeIn:             true,
			MaxReplyChars:       0,
			PreRollAfterPlayMS:  1000,
			BeepPrompt:          false,
			BeepMuteMS:          300,
			PlayBufferMS:        300,
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
	// 密钥单独存放：读得到就合并进内存配置，读不到就看配置里有没有旧的明文（向后兼容）
	sec, serr := LoadSecrets(p)
	if serr == nil && sec != nil {
		ApplySecrets(cfg, sec)
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
	// 先单独加密保存密钥，再写不含密钥的配置文件
	if sec := SecretsFromConfig(c); hasAnySecret(sec) {
		if err := SaveSecrets(path, sec); err != nil {
			return fmt.Errorf("config: 保存密钥失败: %w", err)
		}
	}
	// 写盘前复制一份并剥掉密钥，避免污染内存里的配置
	saved := *c
	ScrubSecrets(&saved)
	raw, err := json.MarshalIndent(&saved, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return err
	}
	_ = os.Chmod(path, 0o660)
	_ = os.Chmod(filepath.Dir(path), 0o750)
	return nil
}

// hasAnySecret 判断是否至少有一个密钥需要保存。
func hasAnySecret(s *Secrets) bool {
	if s == nil {
		return false
	}
	if s.AIKey != "" || s.ASRKey != "" || s.TTSKey != "" || s.RealtimeKey != "" || s.ServerChanKey != "" {
		return true
	}
	for _, v := range s.ProviderKeys {
		if v != "" {
			return true
		}
	}
	return false
}

// Path 返回配置文件路径。
func Path() string {
	mu.RLock()
	defer mu.RUnlock()
	return path
}
