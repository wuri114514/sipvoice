package webui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sipvoice/internal/aibuild"
	"sipvoice/internal/config"
)

// decodeJSON 是 json.NewDecoder 的小包装（限制请求体大小）。
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<20))
	return dec.Decode(v)
}

// analyzeImageFiles 把若干张本地图片（PNG）交给视觉模型分析，合并成一个答案。
func analyzeImageFiles(ctx context.Context, files []string, prompt string) (string, error) {
	cfg := config.Get()
	chat := aibuild.BuildChat(cfg)
	if chat == nil {
		return "", fmt.Errorf("对话模型未配置")
	}
	vc, ok := chat.(interface {
		Vision(ctx context.Context, prompt, dataURL string) (string, error)
	})
	if !ok {
		return "", fmt.Errorf("当前对话模型不支持图片输入，请换成支持视觉的模型（如 qwen3-vl-plus）")
	}

	var parts []string
	for i, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if len(raw) > 10<<20 {
			continue // 超过多数模型的上限，跳过
		}
		mime := "image/png"
		switch strings.ToLower(filepath.Ext(f)) {
		case ".jpg", ".jpeg":
			mime = "image/jpeg"
		}
		dataURL := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw)
		text, err := vc.Vision(ctx, fmt.Sprintf("第 %d/%d 页：%s", i+1, len(files), prompt), dataURL)
		if err != nil {
			return "", fmt.Errorf("第 %d 页分析失败: %w", i+1, err)
		}
		parts = append(parts, strings.TrimSpace(text))
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("没有可分析的页面")
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	return strings.Join(parts, "\n\n"), nil
}

// 供 fax 包调用的保存/读取辅助（放在 webui 侧避免循环依赖时用）。
var _ = time.Now
