// Package voice 实现电话侧的语音对话编排：VAD 断句、打断（barge-in）、
// 处理中提示音、DTMF 控制，以及 ASR → 对话模型 → TTS 的回合处理。
package voice

import (
	"context"
	"encoding/binary"
	"log"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"sipvoice/internal/audiosock"
	"sipvoice/internal/config"
	"sipvoice/internal/media"
	"sipvoice/internal/omni"
	"sipvoice/internal/pipeline"
	"sipvoice/internal/session"
)

const (
	sampleRate       = 8000
	frameMS          = 20
	frameBytes       = 320  // 20ms @ 8k, 16-bit
	bargeGraceFrames = 10   // 播出 ~200ms 后才允许语音打断
	preRollMax       = 8000 // 1 秒预录
)

// Agent 处理一通电话的完整对话。
type Agent struct {
	Cfg  *config.Config
	ASR  pipeline.Transcriber
	Chat pipeline.ChatModel
	TTS  pipeline.Synthesizer
	Obs  Observer

	// Omni 非空时走端到端语音模型（Qwen-Omni-Realtime 等），不再走三段式。
	Omni *omni.Client

	// OnError 在云端调用失败时回调，供上层发告警。
	OnError func(kind, msg string)

	// Sessions 按主叫号码保存对话上下文（异步会话）
	Sessions *session.Manager

	mu sync.RWMutex
}

// SetOnError 设置错误回调（可传 nil）。
func (a *Agent) SetOnError(fn func(kind, msg string)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.OnError = fn
}

func (a *Agent) reportErr(kind string, err error) {
	if err == nil {
		return
	}
	a.mu.RLock()
	fn := a.OnError
	a.mu.RUnlock()
	if fn != nil {
		fn(kind, err.Error())
	}
}

// SetOmni 热更新端到端语音客户端；传 nil 表示关闭。
func (a *Agent) SetOmni(c *omni.Client) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Omni = c
}

func (a *Agent) omniClient() *omni.Client {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.Omni
}

// Observer 接收通话事件，供 WebUI 展示。
type Observer interface {
	CallStart(uuid string)
	CallEnd(uuid string, dur time.Duration, inFrames, outFrames int64)
	Turn(uuid, transcript, reply string)
}

// SetProviders 热更新 AI 适配器（WebUI 保存配置后调用）。
func (a *Agent) SetProviders(asr pipeline.Transcriber, chat pipeline.ChatModel, tts pipeline.Synthesizer) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ASR, a.Chat, a.TTS = asr, chat, tts
}

func (a *Agent) providers() (pipeline.Transcriber, pipeline.ChatModel, pipeline.Synthesizer) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.ASR, a.Chat, a.TTS
}

type frame struct {
	pcm  []int16
	rms  float64
	dtmf string
}

type reply struct {
	transcript string
	answer     string
	pcm8       []int16
}

// Handle 阻塞处理一通电话，直到挂断或 ctx 取消。
func (a *Agent) Handle(ctx context.Context, c *audiosock.Conn) error {
	if latest := config.Get(); latest != nil {
		a.Cfg = latest
	}
	if a.Cfg == nil {
		a.Cfg = config.Default()
	}
	ag := a.Cfg.Agent

	if a.Obs != nil {
		a.Obs.CallStart(c.UUID)
		defer func() {
			a.Obs.CallEnd(c.UUID, time.Since(c.Start), c.FramesIn, c.FramesOut)
		}()
	}

	// 绑定会话：同一主叫号码在 TTL 内共享上下文
	sessKey := ""
	if a.Sessions != nil && c.Caller != "" && a.Cfg.Session.Enabled {
		sessKey = c.Caller
		a.Sessions.SetTTL(time.Duration(a.Cfg.Session.TTLMinutes)*time.Minute, a.Cfg.Session.MaxMessages)
		a.Sessions.Ensure(sessKey)
		defer a.Sessions.EndCall(sessKey)
		log.Printf("[session] 通话绑定会话 %s（历史 %d 条）", sessKey, len(a.Sessions.History(sessKey)))
	}

	in := make(chan frame, 250)
	go a.readLoop(ctx, c, in)
	// 持续发送静音，避免部分 Asterisk 版本（如 20.x）在应用暂时无输出时提前挂断
	go a.fillSilence(ctx, c)

	// 端到端语音模型优先：一条 WebSocket 直接吃音频、吐音频
	if rc := a.omniClient(); rc != nil {
		dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		sess, err := rc.Open(dctx)
		cancel()
		if err != nil {
			log.Printf("[voice] 实时语音会话建立失败，回退三段式: %v", err)
			a.reportErr("realtime", err)
		} else {
			defer sess.Close()
			a.handleRealtime(ctx, c, in, sess, ag)
			return nil
		}
	}

	// 开场问候：播完后会附带一小段预录音，交给 vadLoop 并入第一句
	// 回拨场景（callmap 里有 .flag 标记）已经播过开场白和摘要，这里跳过，直接进 ASR
	var greetingPre []int16
	if isCallbackCall(c.UUID) {
		log.Printf("[voice] 回拨通话，跳过开场问候，直接进入 ASR")
	} else {
		if ag.Greeting != "" {
			if _, _, tts := a.providers(); tts != nil {
				greetingPre = a.say(ctx, c, ag.Greeting, in)
			}
		}
		// 免提场景：播「请在滴声之后开始说话」+ 滴一声，再用屏蔽期吃掉余响，
		// 让用户和 VAD 有个明确的对齐点
		if ag.BeepPrompt {
			greetingPre = a.beepAlign(ctx, c, greetingPre, ag, in)
		}
	}

	a.vadLoop(ctx, c, in, greetingPre, func(seg []int16) (bool, []int16) {
		return a.round(ctx, c, seg, in, sessKey)
	})
	return nil
}

// vadLoop 负责断句：把持续音频切成一句话，交给 onUtterance 处理。
// onUtterance 返回 barge=true 时，pre 里的预录音频会并入下一句。
func (a *Agent) vadLoop(ctx context.Context, c *audiosock.Conn, in <-chan frame,
	initial []int16, onUtterance func(seg []int16) (barge bool, pre []int16)) {

	ag := a.Cfg.Agent
	thr := ag.EnergyThreshold
	if thr <= 0 {
		thr = 700
	}
	silenceLimit := ag.SilenceMS
	if silenceLimit <= 0 {
		silenceLimit = 700
	}
	minSpeech := ag.MinSpeechMS
	if minSpeech <= 0 {
		minSpeech = 200
	}
	maxSamples := ag.MaxUtteranceSeconds * sampleRate
	if maxSamples <= 0 {
		maxSamples = 15 * sampleRate
	}

	var (
		utter   []int16
		silence int
		speech  int
		inUtter bool
	)

	// 开场问候后的预录音直接并入第一句
	if len(initial) > 0 {
		utter = append(utter, initial...)
		inUtter = true
		speech = minSpeech
	}

	for {
		select {
		case <-ctx.Done():
			return
		case f, ok := <-in:
			if !ok {
				return
			}
			if f.dtmf != "" {
				if a.isHangup(f.dtmf) {
					log.Printf("[voice] DTMF %s 挂断", f.dtmf)
					c.Hangup()
					return
				}
				log.Printf("[voice] DTMF %s（空闲态忽略）", f.dtmf)
				continue
			}
			isSpeech := f.rms >= thr
			if isSpeech {
				speech += frameMS
				silence = 0
			} else {
				silence += frameMS
				speech = 0
			}

			if !inUtter {
				if speech >= minSpeech {
					inUtter = true
					utter = append(utter, f.pcm...)
				}
				continue
			}

			utter = append(utter, f.pcm...)
			if len(utter) < maxSamples && silence < silenceLimit {
				continue
			}

			seg := utter
			utter = nil
			inUtter = false
			silence, speech = 0, 0

			barge, pre := onUtterance(seg)
			if barge && len(pre) > 0 {
				utter = append(utter, pre...)
				inUtter = true
				speech = minSpeech
			}
		}
	}
}

// handleRealtime 用端到端语音模型跑完整通电话。
func (a *Agent) handleRealtime(ctx context.Context, c *audiosock.Conn, in <-chan frame, sess *omni.Session, ag config.AgentConfig) {
	if ag.Greeting != "" {
		tctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		turn, err := sess.Say(tctx, "（电话刚接通）请用一句话向用户问好。")
		if err != nil {
			log.Printf("[voice] 实时开场白失败: %v", err)
		} else {
			barge, _, _ := a.playStream(tctx, c, turn.Audio, in)
			if barge {
				sess.Cancel()
			}
			cancel()
			turn.Wait() // 等旧 reader 退出，避免和下一轮抢 socket
		}
		cancel()
	}

	a.vadLoop(ctx, c, in, nil, func(seg []int16) (bool, []int16) {
		t0 := time.Now()
		tctx, cancel := context.WithCancel(ctx)
		turn, err := sess.Ask(tctx, seg)
		if err != nil {
			cancel()
			log.Printf("[voice] 实时请求失败: %v", err)
			a.reportErr("realtime", err)
			return false, nil
		}
		barge, pre, frames := a.playStream(tctx, c, turn.Audio, in)
		if barge {
			sess.Cancel()
			cancel()
		}
		user, reply, terr := turn.Wait()
		first := turn.FirstAudioAt()
		cancel()
		if barge {
			log.Printf("[voice] 实时回合被打断（已播 %.1fs）", float64(frames*frameMS)/1000)
			if a.Obs != nil && (user != "" || reply != "") {
				a.Obs.Turn(c.UUID, user, reply)
			}
			return true, pre
		}
		if terr != nil && terr != context.Canceled {
			log.Printf("[voice] 实时回合错误: %v", terr)
		}
		firstMS := int64(-1)
		if !first.IsZero() {
			firstMS = first.Sub(t0).Milliseconds()
		}
		log.Printf("[voice] 实时回合 识别=%q 回复=%q 首包=%dms 总耗时=%s 音频=%.1fs",
			user, reply, firstMS, time.Since(t0).Round(time.Millisecond), float64(frames*frameMS)/1000)
		if a.Obs != nil {
			a.Obs.Turn(c.UUID, user, reply)
		}
		return false, pre
	})
}

// playStream 边收边播：音频块到达就排队，按 20ms 节奏写出，同时检测打断与 DTMF。
func (a *Agent) playStream(ctx context.Context, c *audiosock.Conn, ch <-chan []int16, in <-chan frame) (bool, []int16, int) {
	const chunk = frameBytes / 2
	ticker := time.NewTicker(frameMS * time.Millisecond)
	defer ticker.Stop()

	bargeIn := a.Cfg.Agent.BargeIn
	thr := a.Cfg.Agent.EnergyThreshold
	if thr <= 0 {
		thr = 700
	}

	var (
		pre  []int16
		buf  []int16
		hot  int
		sent int
	)
	writeChunk := func() bool {
		if len(buf) < chunk {
			return true
		}
		out := buf[:chunk]
		buf = buf[chunk:]
		b := make([]byte, chunk*2)
		for i, v := range out {
			binary.LittleEndian.PutUint16(b[i*2:], uint16(v))
		}
		sent++
		return c.WriteAudio(b) == nil
	}

	for {
		select {
		case <-ctx.Done():
			return false, pre, sent
		case f, ok := <-in:
			if !ok {
				return false, pre, sent
			}
			if f.dtmf != "" {
				if a.isHangup(f.dtmf) {
					c.Hangup()
					return false, pre, sent
				}
				if a.isInterrupt(f.dtmf) {
					log.Printf("[voice] DTMF %s 打断播报", f.dtmf)
					return true, pre, sent
				}
				continue
			}
			pre = append(pre, f.pcm...)
			if len(pre) > preRollMax {
				pre = pre[len(pre)-preRollMax:]
			}
			// 只在"已经播出一点声音"之后才允许打断。
			// 否则模型还在生成时，用户/环境音会把这一轮直接取消，对方只听到静音。
			if bargeIn && sent >= bargeGraceFrames {
				if f.rms >= thr {
					hot++
					if hot >= 3 {
						log.Printf("[voice] 检测到打断（实时）")
						return true, pre, sent
					}
				} else {
					hot = 0
				}
			}
		case pcm, ok := <-ch:
			if !ok || pcm == nil {
				ch = nil // 结束后只把缓冲播完
				continue
			}
			buf = append(buf, pcm...)
		case <-ticker.C:
			if ch == nil && len(buf) < chunk {
				return false, pre, sent
			}
			if !writeChunk() {
				return false, pre, sent
			}
		}
	}
}

// round 处理一个回合：识别、生成、播放。
// 处理期间每 1 秒播一次低音量提示音；支持 DTMF 打断与语音打断。
func (a *Agent) round(ctx context.Context, c *audiosock.Conn, seg []int16, in <-chan frame, sessKey string) (bool, []int16) {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	respCh := make(chan *reply, 1)
	go func() {
		respCh <- a.respond(cctx, seg, sessKey)
	}()

	tone := a.Cfg.Agent.ProcessingTone
	var tickC <-chan time.Time
	if tone.Enabled {
		iv := time.Duration(tone.IntervalMS) * time.Millisecond
		if iv <= 0 {
			iv = time.Second
		}
		ticker := time.NewTicker(iv)
		defer ticker.Stop()
		tickC = ticker.C
		a.playBeep(c, tone) // 先响一下，减少等待焦虑
	}

	thr := a.Cfg.Agent.EnergyThreshold
	if thr <= 0 {
		thr = 700
	}
	hot := 0
	var pre []int16

	for {
		select {
		case <-ctx.Done():
			return false, nil
		case <-tickC:
			a.playBeep(c, tone)
		case f, ok := <-in:
			if !ok {
				return false, nil
			}
			if f.dtmf != "" {
				if a.isHangup(f.dtmf) {
					c.Hangup()
					return false, nil
				}
				if a.isInterrupt(f.dtmf) {
					log.Printf("[voice] DTMF %s 打断处理", f.dtmf)
					cancel()
					return true, pre
				}
				continue
			}
			pre = append(pre, f.pcm...)
			if len(pre) > preRollMax {
				pre = pre[len(pre)-preRollMax:]
			}
			if a.Cfg.Agent.BargeIn {
				if f.rms >= thr {
					hot++
					if hot >= 3 {
						log.Printf("[voice] 检测到打断（处理中）")
						cancel()
						return true, pre
					}
				} else {
					hot = 0
				}
			}
		case r := <-respCh:
			if r == nil {
				return false, nil
			}
			if a.Obs != nil {
				a.Obs.Turn(c.UUID, r.transcript, r.answer)
			}
			return a.play(ctx, c, r.pcm8, in)
		}
	}
}

// fillSilence 保证连接上每 20ms 至少有一帧数据。
// 部分 Asterisk 版本在 AudioSocket 应用"暂时没有输出"时会判定失败并挂断，
// 持续发静音帧可以规避（实测 Ubuntu 24.04 的 Asterisk 20.6 会这样）。
func (a *Agent) fillSilence(ctx context.Context, c *audiosock.Conn) {
	silent := make([]byte, frameBytes)
	tk := time.NewTicker(frameMS * time.Millisecond)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			if time.Since(c.LastWrite()) >= frameMS*time.Millisecond {
				if err := c.WriteAudio(silent); err != nil {
					return
				}
			}
		}
	}
}

// readLoop 把 AudioSocket 帧转成 PCM / DTMF 事件。
func (a *Agent) readLoop(ctx context.Context, c *audiosock.Conn, in chan<- frame) {
	defer close(in)
	for {
		fr, err := c.ReadFrame()
		if err != nil {
			log.Printf("[voice] 读帧结束 uuid=%s: %v", c.UUID, err)
			return
		}
		switch fr.Type {
		case audiosock.TypeSlin8k:
			pcm := make([]int16, len(fr.Payload)/2)
			for i := range pcm {
				pcm[i] = int16(binary.LittleEndian.Uint16(fr.Payload[i*2:]))
			}
			select {
			case in <- frame{pcm: pcm, rms: media.RMS(pcm)}:
			case <-ctx.Done():
				return
			}
		case audiosock.TypeDTMF:
			log.Printf("[voice] 收到 DTMF %q", string(fr.Payload))
			select {
			case in <- frame{dtmf: string(fr.Payload)}:
			case <-ctx.Done():
				return
			}
		case audiosock.TypeHangup:
			log.Printf("[voice] 收到对端挂断帧")
			return
		}
	}
}

// respond 执行 ASR → 对话 → TTS，返回 8k PCM。
func (a *Agent) respond(ctx context.Context, pcm8 []int16, sessKey string) *reply {
	asr, chat, tts := a.providers()
	if asr == nil || chat == nil || tts == nil {
		log.Printf("[voice] AI 未配置，跳过回合")
		return nil
	}
	t0 := time.Now()
	actx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	wav := media.EncodeWAV(pcm8, sampleRate)
	text, err := asr.Transcribe(actx, wav)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		log.Printf("[voice] ASR 失败: %v", err)
		a.reportErr("asr", err)
		return nil
	}
	if text == "" {
		log.Printf("[voice] ASR 结果为空，忽略")
		return nil
	}
	t1 := time.Now()

	// 带历史上下文对话
	var answer string
	if hist, ok := chatHistory(a.Sessions, sessKey); ok {
		if hc, isH := chat.(interface {
			ChatWithHistory(ctx context.Context, system string, history []map[string]string, user string) (string, error)
		}); isH {
			answer, err = hc.ChatWithHistory(actx, a.Cfg.AI.System, hist, text)
		} else {
			answer, err = chat.Chat(actx, a.Cfg.AI.System, text)
		}
	} else {
		answer, err = chat.Chat(actx, a.Cfg.AI.System, text)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		log.Printf("[voice] 对话失败: %v", err)
		a.reportErr("chat", err)
		return nil
	}
	// 记录到会话，供挂断后继续
	if a.Sessions != nil && sessKey != "" {
		a.Sessions.Append(sessKey, session.RoleUser, text, "")
		a.Sessions.Append(sessKey, session.RoleAssistant, answer, "")
		a.Sessions.Touch(sessKey)
		// 识别 AI 回复里的动作指令（发传真 / 回拨）
		a.detectActions(sessKey, answer)
	}
	t2 := time.Now()

	answer = plainText(answer)
	if max := a.Cfg.Agent.MaxReplyChars; max > 0 {
		answer = shorten(answer, max)
	}
	audio, err := tts.Speak(actx, answer)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		log.Printf("[voice] TTS 失败: %v", err)
		a.reportErr("tts", err)
		return nil
	}
	pcm, rate, err := media.DecodeWAV(audio)
	if err != nil {
		log.Printf("[voice] TTS 音频解析失败: %v", err)
		return nil
	}
	if rate != sampleRate {
		pcm = media.Resample(pcm, rate, sampleRate)
	}
	log.Printf("[voice] 回合 识别=%q 回复=%q ASR=%s LLM=%s TTS=%s 音频=%.1fs",
		text, answer, t1.Sub(t0).Round(time.Millisecond), t2.Sub(t1).Round(time.Millisecond),
		time.Since(t2).Round(time.Millisecond), float64(len(pcm))/sampleRate)
	return &reply{transcript: text, answer: answer, pcm8: pcm}
}

// plainText 去掉 Markdown 标记，便于语音朗读。
func plainText(s string) string {
	repl := strings.NewReplacer(
		"**", "", "__", "", "", "", "###", "", "##", "", "#", "",
		"* ", "", "- ", "", "> ", "",
	)
	s = repl.Replace(s)
	s = strings.ReplaceAll(s, "\r", "")
	lines := strings.Split(s, "\n")
	var out []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "。")
}

// shorten 把过长的回复截断（仅当配置了 MaxReplyChars 时启用）。
func shorten(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	cut := r[:max]
	for i := len(cut) - 1; i > max/2; i-- {
		if strings.ContainsRune("。！？；", cut[i]) {
			return string(cut[:i+1])
		}
	}
	return string(cut) + "。"
}

// say 合成并播放一段固定文本（开场白等）。
func (a *Agent) say(ctx context.Context, c *audiosock.Conn, text string, in <-chan frame) []int16 {
	_, _, tts := a.providers()
	if tts == nil {
		return nil
	}
	actx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	audio, err := tts.Speak(actx, text)
	if err != nil {
		log.Printf("[voice] 开场白 TTS 失败: %v", err)
		return nil
	}
	pcm, rate, err := media.DecodeWAV(audio)
	if err != nil {
		log.Printf("[voice] 开场白音频解析失败: %v", err)
		return nil
	}
	if rate != sampleRate {
		pcm = media.Resample(pcm, rate, sampleRate)
	}
	_, pre := a.play(ctx, c, pcm, in)
	return pre
}

// play 按 20ms 节奏播放 PCM，同时检测打断与 DTMF。
func (a *Agent) play(ctx context.Context, c *audiosock.Conn, pcm8 []int16, in <-chan frame) (bool, []int16) {
	const chunk = frameBytes / 2
	ticker := time.NewTicker(frameMS * time.Millisecond)
	defer ticker.Stop()

	// 播放缓冲：先把前 0.3 秒音频推下去再开始节拍发送。
	// 手机/软电话的抖动缓冲需要一点"预灌"才不会在句中间断流，
	// 断流听感就是吞字、像嗓子里卡了东西。
	bufMS := a.Cfg.Agent.PlayBufferMS
	if bufMS < 0 {
		bufMS = 0
	}
	if bufMS > 1000 {
		bufMS = 1000
	}
	if bufMS > 0 && len(pcm8) > 0 {
		prefill := bufMS * sampleRate / 1000
		if prefill > len(pcm8) {
			prefill = len(pcm8)
		}
		pb := make([]byte, prefill*2)
		for i, v := range pcm8[:prefill] {
			binary.LittleEndian.PutUint16(pb[i*2:], uint16(v))
		}
		if err := c.WriteAudio(pb); err != nil {
			return false, nil
		}
		// 用新的切片继续，预灌部分不再重复发送
		pcm8 = pcm8[prefill:]
	}

	bargeIn := a.Cfg.Agent.BargeIn
	thr := a.Cfg.Agent.EnergyThreshold
	if thr <= 0 {
		thr = 700
	}

	var pre []int16
	hot := 0
	for idx := 0; idx < len(pcm8); {
		for len(in) > 0 {
			f, ok := <-in
			if !ok {
				return false, pre
			}
			if f.dtmf != "" {
				if a.isHangup(f.dtmf) {
					c.Hangup()
					return false, pre
				}
				if a.isInterrupt(f.dtmf) {
					log.Printf("[voice] DTMF %s 打断播报", f.dtmf)
					return true, pre
				}
				continue
			}
			pre = append(pre, f.pcm...)
			if len(pre) > preRollMax {
				pre = pre[len(pre)-preRollMax:]
			}
			if bargeIn {
				if f.rms >= thr {
					hot++
					if hot >= 3 {
						log.Printf("[voice] 检测到打断")
						return true, pre
					}
				} else {
					hot = 0
				}
			}
		}

		end := idx + chunk
		if end > len(pcm8) {
			end = len(pcm8)
		}
		buf := make([]byte, (end-idx)*2)
		for i, v := range pcm8[idx:end] {
			binary.LittleEndian.PutUint16(buf[i*2:], uint16(v))
		}
		if err := c.WriteAudio(buf); err != nil {
			return false, pre
		}
		idx = end

		select {
		case <-ctx.Done():
			return false, pre
		case <-ticker.C:
		}
	}

	// 结尾补一点静音，避免最后几十毫秒因为缓冲欠载被吞掉
	if bufMS > 0 {
		tail := sampleRate * bufMS / 1000
		if tail > 0 {
			silence := make([]byte, tail*2)
			_ = c.WriteAudio(silence)
		}
	}

	// 播完后继续采集一小段（默认 1 秒）作为预录音，避免用户抢话时开头几个字被丢掉
	if preRollMS := a.Cfg.Agent.PreRollAfterPlayMS; preRollMS > 0 {
		collect := time.NewTicker(frameMS * time.Millisecond)
		defer collect.Stop()
		deadline := time.Now().Add(time.Duration(preRollMS) * time.Millisecond)
		for time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return false, pre
			case f, ok := <-in:
				if !ok {
					return false, pre
				}
				if f.dtmf != "" {
					continue
				}
				pre = append(pre, f.pcm...)
				if len(pre) > preRollMax {
					pre = pre[len(pre)-preRollMax:]
				}
			case <-collect.C:
			}
		}
	}
	return false, pre
}

// playBeep 播放一次"处理中"提示音。
func (a *Agent) playBeep(c *audiosock.Conn, tone config.ToneConfig) {
	dur := tone.DurationMS
	if dur <= 0 {
		dur = 120
	}
	freq := tone.Freq
	if freq <= 0 {
		freq = 800
	}
	vol := tone.Volume
	if vol <= 0 || vol > 1 {
		vol = 0.15
	}
	n := sampleRate * dur / 1000
	pcm := make([]int16, n)
	amp := vol * 32767
	for i := 0; i < n; i++ {
		pcm[i] = int16(amp * math.Sin(2*math.Pi*freq*float64(i)/sampleRate))
	}
	const chunk = frameBytes / 2
	for off := 0; off < len(pcm); off += chunk {
		end := off + chunk
		if end > len(pcm) {
			end = len(pcm)
		}
		buf := make([]byte, (end-off)*2)
		for i, v := range pcm[off:end] {
			binary.LittleEndian.PutUint16(buf[i*2:], uint16(v))
		}
		if err := c.WriteAudio(buf); err != nil {
			return
		}
	}
}

func (a *Agent) isInterrupt(d string) bool {
	dt := a.Cfg.Agent.DTMF
	return dt.Enabled && dt.Interrupt != "" && d == dt.Interrupt
}

func (a *Agent) isHangup(d string) bool {
	dt := a.Cfg.Agent.DTMF
	return dt.Enabled && dt.Hangup != "" && d == dt.Hangup
}

// chatHistory 把会话历史转成模型需要的 messages 数组。
func chatHistory(m *session.Manager, key string) ([]map[string]string, bool) {
	if m == nil || key == "" {
		return nil, false
	}
	msgs := m.History(key)
	if len(msgs) == 0 {
		return nil, false
	}
	out := make([]map[string]string, 0, len(msgs))
	for _, x := range msgs {
		out = append(out, map[string]string{"role": x.Role, "content": x.Content})
	}
	return out, true
}

// actionMarker 是 AI 用来表达“要做事”的约定格式。
// 让模型输出 [FAX:号码:文件名] 或 [CALLBACK:要说的话] 这样的一行，
// 我们把它从朗读文本里去掉，转成会话任务。
func (a *Agent) detectActions(sessKey, reply string) {
	if a.Sessions == nil || sessKey == "" {
		return
	}
	type action struct {
		kind string
		args string
	}
	var acts []action
	out := reply
	for {
		i := strings.Index(out, "[")
		j := strings.Index(out, "]")
		if i < 0 || j < 0 || j < i {
			break
		}
		inner := out[i+1 : j]
		switch {
		case strings.HasPrefix(inner, "FAX:") || strings.HasPrefix(inner, "fax:"):
			acts = append(acts, action{kind: "fax", args: inner[4:]})
		case strings.HasPrefix(inner, "FAXTEXT:") || strings.HasPrefix(inner, "faxtext:"):
			acts = append(acts, action{kind: "faxtext", args: inner[9:]})
		case strings.HasPrefix(inner, "CALLBACK:") || strings.HasPrefix(inner, "callback:"):
			acts = append(acts, action{kind: "callback", args: inner[9:]})
		}
		out = out[:i] + out[j+1:]
	}
	for _, act := range acts {
		parts := strings.SplitN(act.args, ":", 2)
		switch act.kind {
		case "faxtext":
			// 格式 [FAXTEXT:目标号码:要传真的文本]
			if len(parts) < 2 {
				continue
			}
			dest := strings.TrimSpace(parts[0])
			body := strings.TrimSpace(parts[1])
			log.Printf("[voice] 识别到文本传真指令 → %s（%d 字）", dest, len([]rune(body)))
			a.Sessions.AddTask(sessKey, session.Task{
				Type: "fax",
				Params: map[string]any{
					"dest": dest,
					"text": body,
				},
			})
		case "fax":
			// 格式 [FAX:目标号码:文件名]
			if len(parts) < 2 {
				continue
			}
			dest := strings.TrimSpace(parts[0])
			file := strings.TrimSpace(parts[1])
			log.Printf("[voice] 识别到发传真指令 → %s（%s）", dest, file)
			a.Sessions.AddTask(sessKey, session.Task{
				Type: "fax",
				Params: map[string]any{
					"dest": dest,
					"file": file,
				},
			})
		case "callback":
			log.Printf("[voice] 识别到回拨指令：%s", act.args)
			a.Sessions.AddTask(sessKey, session.Task{
				Type: "callback",
				Params: map[string]any{
					"message": strings.TrimSpace(act.args),
				},
			})
		}
	}
}

// isCallbackCall 判断这通电话是不是 AI 主动回拨（拨号方案会写 .flag 标记）。
func isCallbackCall(uuid string) bool {
	return audiosock.IsFlagged(uuid)
}

// beepAlign 播「请在滴声之后开始说话」→ 滴一声 → 屏蔽一段时间吃掉扬声器余响。
// 返回的 pre 会并入第一句，避免用户听到滴就开口时丢字。
func (a *Agent) beepAlign(ctx context.Context, c *audiosock.Conn, pre []int16,
	ag config.AgentConfig, in <-chan frame) []int16 {

	// 滴一声（800Hz 短音，免提下也听得清）
	// 提示语交给开场白自己说，这里不再重复播放
	beep := synthBeep(800, sampleRate) // 800Hz / 100ms
	if _, p2 := a.play(ctx, c, beep, in); len(p2) > 0 {
		pre = append(pre, p2...)
	}

	// 屏蔽期：这段时间收到的音频不算"用户开始说话"，但仍保留在 pre 里
	muteMS := ag.BeepMuteMS
	if muteMS < 0 {
		muteMS = 0
	}
	if muteMS > 3000 {
		muteMS = 3000
	}
	if muteMS > 0 {
		tm := time.NewTimer(time.Duration(muteMS) * time.Millisecond)
		defer tm.Stop()
		for {
			select {
			case <-ctx.Done():
				return pre
			case f, ok := <-in:
				if !ok {
					return pre
				}
				if f.dtmf != "" {
					continue
				}
				pre = append(pre, f.pcm...)
				if len(pre) > preRollMax {
					pre = pre[len(pre)-preRollMax:]
				}
			case <-tm.C:
				log.Printf("[voice] 滴声后屏蔽 %dms 结束，开始正常收音", muteMS)
				return pre
			}
		}
	}
	return pre
}

// synthBeep 生成一段短提示音（16bit 单声道 PCM，带淡入淡出避免爆音）。
func synthBeep(freq, rate int) []int16 {
	n := rate / 10 // 100ms
	out := make([]int16, n)
	fade := rate / 100 // 10ms
	for i := 0; i < n; i++ {
		v := math.Sin(2 * math.Pi * float64(freq) * float64(i) / float64(rate))
		amp := 0.35
		if i < fade {
			amp *= float64(i) / float64(fade)
		}
		if i > n-fade {
			amp *= float64(n-i) / float64(fade)
		}
		out[i] = int16(v * amp * 32767)
	}
	return out
}

// loadPromptWAV 读取预生成的提示音频（8k 单声道 wav）。
// 直接用 media.DecodeWAV 解析，读不到就返回 false 让调用方现合成。
func loadPromptWAV(path string) ([]int16, bool) {
	if path == "" {
		return nil, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	pcm, rate, err := media.DecodeWAV(raw)
	if err != nil {
		return nil, false
	}
	if rate != sampleRate {
		pcm = media.Resample(pcm, rate, sampleRate)
	}
	return pcm, len(pcm) > 0
}
