package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Secrets 是单独存放的密钥文件（仅 root 可读，服务启动时一次性读取）。
//
// 设计要点：
//   - 配置文件 config.json 里不再有明文密钥，只有空串；
//   - secrets.json 由机器 ID 派生的密钥做 AES-GCM 加密，并限定 0600 root:root；
//   - 服务以 sipvoice 身份运行，启动时通过 sudo 读一次，之后只存在内存里。
type Secrets struct {
	// AI 主密钥
	AIKey string `json:"ai_key"`
	// 各服务商独立密钥
	ProviderKeys map[string]string `json:"provider_keys"`
	// ASR / TTS 密钥
	ASRKey string `json:"asr_key"`
	TTSKey string `json:"tts_key"`
	// 端到端语音密钥
	RealtimeKey string `json:"realtime_key"`
	// 推送密钥
	ServerChanKey string `json:"serverchan_key"`
}

// SecretsPath 返回密钥文件路径（与配置文件同目录）。
func SecretsPath(configPath string) string {
	dir := filepath.Dir(configPath)
	return filepath.Join(dir, "secrets.json")
}

// machineKey 用机器标识派生 AES-256 密钥。
// 换机器后旧文件解不开——这是有意的：密钥不应该跟着配置一起被拷走。
func machineKey() []byte {
	id := ""
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if raw, err := os.ReadFile(p); err == nil {
			id = strings.TrimSpace(string(raw))
			break
		}
	}
	if id == "" {
		id = "sipvoice-fallback-machine-id"
	}
	sum := sha256.Sum256([]byte("sipvoice-secrets-v1|" + id))
	return sum[:]
}

// EncryptSecrets 把 Secrets 序列化并加密成可直接落盘的字节。
func EncryptSecrets(s *Secrets) ([]byte, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(machineKey())
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nonce, nonce, raw, nil)
	// 用 base64 包一层，避免二进制内容在编辑器里被改坏
	out := base64.StdEncoding.EncodeToString(sealed)
	return []byte(out + "\n"), nil
}

// DecryptSecrets 解析并解密密钥文件。
func DecryptSecrets(raw []byte) (*Secrets, error) {
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("secrets: base64 解析失败: %w", err)
	}
	block, err := aes.NewCipher(machineKey())
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(data) < gcm.NonceSize() {
		return nil, fmt.Errorf("secrets: 文件长度不足")
	}
	nonce, ct := data[:gcm.NonceSize()], data[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("secrets: 解密失败（换机器或文件损坏）: %w", err)
	}
	var s Secrets
	if err := json.Unmarshal(plain, &s); err != nil {
		return nil, fmt.Errorf("secrets: 解析失败: %w", err)
	}
	return &s, nil
}

// LoadSecrets 读取密钥文件；不存在时返回空 Secrets（视为未配置）。
func LoadSecrets(configPath string) (*Secrets, error) {
	raw, err := os.ReadFile(SecretsPath(configPath))
	if err != nil {
		if os.IsNotExist(err) {
			return &Secrets{ProviderKeys: map[string]string{}}, nil
		}
		return nil, err
	}
	return DecryptSecrets(raw)
}

// SaveSecrets 加密写入密钥文件（0600）。
func SaveSecrets(configPath string, s *Secrets) error {
	enc, err := EncryptSecrets(s)
	if err != nil {
		return err
	}
	p := SecretsPath(configPath)
	if err := os.WriteFile(p, enc, 0o600); err != nil {
		return err
	}
	return os.Chmod(p, 0o600)
}

// SecretsFromConfig 从内存配置里抽出所有密钥。
func SecretsFromConfig(c *Config) *Secrets {
	s := &Secrets{
		AIKey:         c.AI.APIKey,
		ASRKey:        c.AI.ASRKey,
		TTSKey:        c.AI.TTSKey,
		RealtimeKey:   c.Realtime.APIKey,
		ServerChanKey: c.Notify.ServerChanKey,
		ProviderKeys:  map[string]string{},
	}
	for k, v := range c.AI.ProviderKeys {
		s.ProviderKeys[k] = v
	}
	return s
}

// ApplySecrets 把密钥填回配置对象（另一个方向）。
func ApplySecrets(c *Config, s *Secrets) {
	if s == nil {
		return
	}
	c.AI.APIKey = s.AIKey
	c.AI.ASRKey = s.ASRKey
	c.AI.TTSKey = s.TTSKey
	c.Realtime.APIKey = s.RealtimeKey
	c.Notify.ServerChanKey = s.ServerChanKey
	if c.AI.ProviderKeys == nil {
		c.AI.ProviderKeys = map[string]string{}
	}
	for k, v := range s.ProviderKeys {
		c.AI.ProviderKeys[k] = v
	}
}

// ScrubSecrets 清空配置对象里的所有密钥（落盘前调用）。
func ScrubSecrets(c *Config) {
	c.AI.APIKey = ""
	c.AI.ASRKey = ""
	c.AI.TTSKey = ""
	c.Realtime.APIKey = ""
	c.Notify.ServerChanKey = ""
	c.AI.ProviderKeys = nil
}
