package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sipvoice/internal/aibuild"
	"sipvoice/internal/config"
)

// visionFile 把一张本地图片交给当前配置的视觉模型，返回回答。
func visionFile(ctx context.Context, path, prompt string) (string, error) {
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
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读取图片失败: %w", err)
	}
	if len(raw) > 10<<20 {
		return "", fmt.Errorf("图片 %.1f MB 超过模型上限", float64(len(raw))/(1<<20))
	}
	mime := "image/png"
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		mime = "image/jpeg"
	}
	dataURL := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw)
	return vc.Vision(ctx, prompt, dataURL)
}
