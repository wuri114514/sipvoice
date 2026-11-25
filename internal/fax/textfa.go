package fax

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// TextToTIFF 把一段文本（可以是代码、说明）渲染成传真用的 TIFF。
// 中文靠 paps（pango 渲染）+ Noto CJK 字体，没有 paps 时退回 enscript（仅 ASCII）。
func TextToTIFF(text, outPath string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("文本为空")
	}
	tmpDir, err := os.MkdirTemp("", "sipvoice-pdf-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	txtFile := filepath.Join(tmpDir, "doc.txt")
	if err := os.WriteFile(txtFile, []byte(text), 0o644); err != nil {
		return err
	}
	pdfFile := filepath.Join(tmpDir, "doc.pdf")
	psFile := filepath.Join(tmpDir, "doc.ps")

	// 优先 paps（支持中文）
	if _, err := exec.LookPath("paps"); err == nil {
		ps, err := exec.Command("paps", "--font=Noto Sans CJK SC 10", txtFile).Output()
		if err != nil {
			return fmt.Errorf("paps 失败: %w", err)
		}
		if err := os.WriteFile(psFile, ps, 0o644); err != nil {
			return err
		}
	} else if _, err := exec.LookPath("enscript"); err == nil {
		out, err := exec.Command("enscript", "-q", "-B", "-f", "Courier8", "-o", "-", txtFile).Output()
		if err != nil {
			return fmt.Errorf("enscript 失败: %w", err)
		}
		if err := os.WriteFile(psFile, out, 0o644); err != nil {
			return err
		}
	} else {
		return fmt.Errorf("缺少 paps/enscript，无法生成 PDF")
	}

	if out, err := exec.Command("ps2pdf", psFile, pdfFile).CombinedOutput(); err != nil {
		return fmt.Errorf("ps2pdf 失败: %v %s", err, strings.TrimSpace(string(out)))
	}

	// PDF → 多页 TIFF（G4 压缩，传真的标准格式）
	cmd := exec.Command("convert", "-density", "204", pdfFile,
		"-monochrome", "-compress", "Group4", outPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("convert 失败: %v %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// TextToFaxAndSend 把文本转成传真并发给指定号码。
// 生成的 TIFF 落在 spool 目录，便于后续重发与列表展示。
func TextToFaxAndSend(text, dest string) SendResult {
	start := time.Now()
	cfg := Load()
	name := fmt.Sprintf("ai-%s-to%s.tiff", time.Now().Format("20060102-150405"), dest)
	out := filepath.Join(cfg.Spool, name)
	if err := TextToTIFF(text, out); err != nil {
		return finish(false, "生成传真失败: "+err.Error(), start)
	}
	_ = os.Chmod(out, 0o664)
	_ = exec.Command("chgrp", "sipvoice", out).Run()
	return Send(cfg, name, dest)
}
