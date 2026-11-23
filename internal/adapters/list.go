package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// NormalizeBase 把用户填写的 Base URL 规整成可直接拼接接口路径的形式：
//   - 去掉末尾多余的 /；
//   - 只有域名（Path 为空或 "/"）时补上 /v1，兼容 DeepSeek、硅基流动等常见写法。
func NormalizeBase(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	b := strings.TrimRight(raw, "/")
	u, err := url.Parse(b)
	if err != nil || u.Host == "" {
		return b
	}
	if u.Path == "" || u.Path == "/" {
		return b + "/v1"
	}
	return b
}

// modelCache 缓存模型列表，避免每次打开面板都打一次远端接口。
type modelCacheEntry struct {
	models []string
	at     time.Time
}

var modelCache = struct {
	m map[string]modelCacheEntry
}{m: map[string]modelCacheEntry{}}

const modelCacheTTL = 5 * time.Minute

// ListModels 在线拉取服务商的模型列表。
// protocol = "ollama" 时走 /api/tags，其余按 OpenAI 兼容的 GET {base}/models 解析。
// useCache 为 true 时 5 分钟内复用上次结果。
func ListModels(ctx context.Context, protocol, baseURL, apiKey string, useCache bool) ([]string, error) {
	base := NormalizeBase(baseURL)
	if base == "" {
		return nil, fmt.Errorf("未填写 Base URL")
	}
	key := base + "\x00" + apiKey
	if useCache {
		if e, ok := modelCache.m[key]; ok && time.Since(e.at) < modelCacheTTL {
			return append([]string(nil), e.models...), nil
		}
	}

	var (
		models []string
		err    error
	)
	if protocol == "ollama" {
		models, err = listOllama(ctx, base, apiKey)
	} else {
		models, err = listOpenAI(ctx, base, apiKey)
	}
	if err != nil {
		return nil, err
	}
	sort.Strings(models)

	modelCache.m[key] = modelCacheEntry{models: append([]string(nil), models...), at: time.Now()}
	return models, nil
}

func listOpenAI(ctx context.Context, base, apiKey string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := sharedClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(raw))
	}

	// 常见形态：{"data":[{"id":"..."}]}
	var wrap struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &wrap); err == nil && len(wrap.Data) > 0 {
		out := make([]string, 0, len(wrap.Data))
		for _, m := range wrap.Data {
			if m.ID != "" {
				out = append(out, m.ID)
			}
		}
		return out, nil
	}
	// 少数服务直接返回字符串数组。
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr, nil
	}
	return nil, fmt.Errorf("无法解析模型列表：%s", truncate(raw))
}

func listOllama(ctx context.Context, base, apiKey string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	root := strings.TrimSuffix(base, "/v1")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, root+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := sharedClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(raw))
	}
	var out struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析 Ollama 模型列表: %w", err)
	}
	res := make([]string, 0, len(out.Models))
	for _, m := range out.Models {
		if m.Name != "" {
			res = append(res, m.Name)
		}
	}
	return res, nil
}

// FilterByKind 按用途粗筛模型名：chat / asr / tts。
// 筛不出结果时返回原始列表，避免用户被空列表卡住。
func FilterByKind(kind string, all []string) []string {
	keep := func(id string) bool {
		s := strings.ToLower(id)
		switch kind {
		case "chat":
			for _, bad := range []string{"embed", "rerank", "tts", "asr", "whisper", "sensevoice", "cosyvoice", "stable-diffusion", "flux"} {
				if strings.Contains(s, bad) {
					return false
				}
			}
			return true
		case "asr":
			for _, good := range []string{"whisper", "sensevoice", "asr", "paraformer", "transcrib", "speech-to-text"} {
				if strings.Contains(s, good) {
					return true
				}
			}
			return false
		case "tts":
			if strings.Contains(s, "speech-to-text") {
				return false
			}
			for _, good := range []string{"tts", "cosyvoice", "speech"} {
				if strings.Contains(s, good) {
					return true
				}
			}
			return false
		}
		return true
	}
	out := make([]string, 0, len(all))
	for _, id := range all {
		if keep(id) {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return all
	}
	return out
}
