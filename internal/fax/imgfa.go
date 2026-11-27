package fax

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// 传真页面按 A4、204dpi 输出：1728 x 2292 像素，这是 G3 传真的事实标准。
const (
	faxPageWidth  = 1728
	faxPageHeight = 2292
	faxPageDPI    = "204"
)

// ImageToTIFF 把一批图片（或 PDF）合成一份传真用的多页 TIFF。
//
// 处理链：自动定向 → 白底去透明 → 灰度 → 缩放到 A4@204dpi → 居中补白 →
// 对比度拉伸 → 轻锐化 → 二值化 → G4 压缩。
// 手机拍的照片、彩色截图、带灰底的扫描件走一遍，传真机上就是干净的黑白页。
func ImageToTIFF(inputs []string, outPath string) error {
	if len(inputs) == 0 {
		return fmt.Errorf("没有可转换的图片")
	}
	if _, err := exec.LookPath("convert"); err != nil {
		return fmt.Errorf("缺少 ImageMagick（convert），无法处理图片")
	}
	args := imageToTIFFArgs(inputs, outPath)
	out, err := exec.Command("convert", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("图片转换失败: %v %s", err, strings.TrimSpace(string(out)))
	}
	fi, err := os.Stat(outPath)
	if err != nil || fi.Size() == 0 {
		return fmt.Errorf("图片转换失败：输出文件为空")
	}
	return nil
}

// imageToTIFFArgs 生成 convert 的参数（拆出来便于测试，不依赖本机装没装 ImageMagick）。
func imageToTIFFArgs(inputs []string, out string) []string {
	args := []string{"-density", faxPageDPI}
	args = append(args, inputs...)
	args = append(args,
		"-auto-orient",
		"-background", "white",
		"-alpha", "remove",
		"-colorspace", "Gray",
		"-resize", fmt.Sprintf("%dx%d", faxPageWidth, faxPageHeight),
		"-gravity", "center",
		"-extent", fmt.Sprintf("%dx%d", faxPageWidth, faxPageHeight),
		// 自动黑白化：切掉两端 2% 的极值后再拉伸，手机照片的灰底会变纯白
		"-contrast-stretch", "2%x2%",
		"-sharpen", "0x1",
		"-threshold", "55%",
		"-type", "bilevel",
		"-units", "PixelsPerInch",
		"-density", faxPageDPI,
		"-compress", "Group4",
		out,
	)
	return args
}
