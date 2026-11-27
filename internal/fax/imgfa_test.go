package fax

import (
	"strings"
	"testing"
)

// 传真页面的转换参数要稳定：A4@204dpi、灰度、二值化、G4 压缩，
// 且输入顺序不能乱（多张图就是多页）。
func TestImageToTIFFArgs(t *testing.T) {
	args := imageToTIFFArgs([]string{"1.jpg", "2.png"}, "/tmp/out.tiff")
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"-colorspace Gray",
		"-threshold 55%",
		"-type bilevel",
		"-compress Group4",
		"-contrast-stretch 2%x2%",
		"-resize 1728x2292",
		"-extent 1728x2292",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("参数里缺少 %q\n实际: %s", want, joined)
		}
	}

	if args[len(args)-1] != "/tmp/out.tiff" {
		t.Errorf("输出文件必须是最后一个参数，实际 %q", args[len(args)-1])
	}

	iIn := indexOf(args, "1.jpg")
	iSecond := indexOf(args, "2.png")
	iOrient := indexOf(args, "-auto-orient")
	if iIn < 0 || iSecond < 0 || iOrient < 0 || iIn > iOrient || iSecond > iOrient {
		t.Errorf("输入图片必须排在处理参数之前: %s", joined)
	}
	if iIn > iSecond {
		t.Errorf("多张图片的顺序被改变: %s", joined)
	}
}

func TestImageToTIFFRejectsEmpty(t *testing.T) {
	if err := ImageToTIFF(nil, "/tmp/x.tiff"); err == nil {
		t.Error("没有输入图片时应该报错")
	}
}

func indexOf(xs []string, v string) int {
	for i, x := range xs {
		if x == v {
			return i
		}
	}
	return -1
}
