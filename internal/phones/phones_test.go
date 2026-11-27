package phones

import (
	"path/filepath"
	"strings"
	"testing"
)

// 回归：控制台「保存话机」会整份重写 extensions.conf。
// 模板里必须自带「主叫号码映射」和传真拨号方案的 include，否则保存一次话机就：
//   - 9000 进 AudioSocket 时没有 caller，会话记忆与自动回拨静默失效；
//   - [fax-send] / [fax-callback] 从运行中的 dialplan 里消失。
func TestRenderExtensionsKeepsCallerMapAndFaxInclude(t *testing.T) {
	st := New(filepath.Join(t.TempDir(), "phones.json"))
	out := st.RenderExtensions()

	for _, want := range []string{
		"Set(ACALLER=${CALLERID(num)})",
		"/var/lib/sipvoice/callmap/${AUUID}",
		"AudioSocket(${AUUID},127.0.0.1:9092)",
		"#include extensions_sipfax.conf",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("extensions.conf 缺少 %q\n---- 实际内容 ----\n%s", want, out)
		}
	}

	// 顺序不能反：映射文件必须在进 AudioSocket 之前写好，Go 侧才查得到
	iMap := strings.Index(out, "callmap/${AUUID}")
	iSock := strings.Index(out, "AudioSocket(${AUUID}")
	if iMap < 0 || iSock < 0 || iMap > iSock {
		t.Fatalf("callmap 写入必须在 AudioSocket 之前（map=%d sock=%d）", iMap, iSock)
	}

	// 回归：早期模板用双引号包 callmap 路径会被 System() 当成 Asterisk 变量，
	// 这里确认整行是标准写法
	if !strings.Contains(out, "System(/bin/sh -c 'echo ${ACALLER} > /var/lib/sipvoice/callmap/${AUUID}')") {
		t.Fatalf("callmap 写入行格式不对\n%s", out)
	}
}

// 互播策略不该被这次改动破坏。
func TestRenderExtensionsKeepsIntercall(t *testing.T) {
	st := New(filepath.Join(t.TempDir(), "phones.json"))
	// New() 在文件不存在时会写入 1001/1002 两台默认话机
	out := st.RenderExtensions()
	if !strings.Contains(out, "exten => 1002,1,Dial(PJSIP/1002,30)") {
		t.Fatalf("缺少互播规则\n%s", out)
	}
	if !strings.Contains(out, "[ctx-1001]") || !strings.Contains(out, "[ctx-1002]") {
		t.Fatalf("缺少每台话机的拨号上下文\n%s", out)
	}
}
