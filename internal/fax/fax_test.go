package fax

import (
	"strings"
	"testing"
)

func testConfig() Config {
	c := Config{
		Enabled:       true,
		InboundExt:    "600",
		ECM:           false,
		PromptEnabled: true,
		PromptFile:    "/var/lib/sipvoice/prompts/fax-intro.wav",
		PromptWait:    30,
		HangupDelay:   3,
	}
	c.Normalize()
	return c
}

// 回归：控制台保存「传真」页会整份重写 extensions_sipfax.conf。
// 生成器必须同时产出 [fax-send] 和 [fax-callback]，否则一次保存就把 AI 回拨
// 上下文删掉，worker 的 originate 直接报 "no such context"，表现为「AI 不回拨」。
func TestRenderDialplanIncludesCallbackContext(t *testing.T) {
	out := RenderDialplan(testConfig())

	for _, want := range []string{
		"[fax-send]",
		"[fax-callback]",
		"SendFAX(",
		"exten => reply,1,NoOp(SIPVoice AI callback)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("extensions_sipfax.conf 缺少 %q\n---- 实际内容 ----\n%s", want, out)
		}
	}

	// 回拨里必须写 uuid -> 主叫号码 并打 callback 标记：
	// caller 决定能不能接着同一条会话（AI 记得那页传真），flag 决定跳过开场问候。
	iMap := strings.Index(out, "callmap/${AUUID}")
	if iMap < 0 {
		t.Fatalf("回拨上下文没有写 callmap\n%s", out)
	}
	if !strings.Contains(out, "/${AUUID}.flag") {
		t.Fatalf("回拨上下文没有打 callback 标记\n%s", out)
	}

	// 映射必须在 AudioSocket 之前
	iSock := strings.Index(out, "AudioSocket(${AUUID}")
	if iSock < 0 || iMap > iSock {
		t.Fatalf("callmap 必须写在 AudioSocket 之前（map=%d sock=%d）", iMap, iSock)
	}
}

// 关闭提示音时 [fax-send] 不应生成播放/等待，避免在真实通话里空等。
func TestRenderDialplanWithoutPrompt(t *testing.T) {
	c := testConfig()
	c.PromptEnabled = false
	out := RenderDialplan(c)
	send := out
	if i := strings.Index(out, "[fax-callback]"); i >= 0 {
		send = out[:i]
	}
	if strings.Contains(send, "WaitExten(") || strings.Contains(send, "Playback(") {
		t.Fatalf("关闭提示音后 [fax-send] 仍在播提示\n%s", send)
	}
	if !strings.Contains(send, "SendFAX(") {
		t.Fatalf("发送逻辑缺失\n%s", send)
	}
}

// ECM 打开时不应写 FAXOPT(ecm)=no。
func TestRenderDialplanECM(t *testing.T) {
	c := testConfig()
	c.ECM = true
	if out := RenderDialplan(c); strings.Contains(out, "Set(FAXOPT(ecm)=no)") {
		t.Fatalf("ECM 打开时不应关闭 ECM\n%s", out)
	}
	c.ECM = false
	if out := RenderDialplan(c); !strings.Contains(out, "Set(FAXOPT(ecm)=no)") {
		t.Fatalf("ECM 关闭时应写 FAXOPT(ecm)=no\n%s", out)
	}
}
