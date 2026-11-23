// Package voice 实现电话侧的语音对话编排：VAD 断句、打断（barge-in）、
// 处理中提示音、DTMF 控制，以及 ASR → 对话模型 → TTS 的回合处理。
package voice

import (
	"context"
	"encoding/binary"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"sipvoice/internal/audiosock"
	"sipvoice/internal/config"
	"sipvoice/internal/media"
	"sipvoice/internal/omni"
	"sipvoice/internal/pipeline"
)

const (
	sampleRate = 8000
	frameMS    = 20
	frameBytes = 320  // 20ms @ 8k, 16-bit
	preRollMax = 8000 // 1 秒预录
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

	in := make(chan frame, 250)
	go a.readLoop(ctx, c, in)

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

	if ag.Greeting != "" {
		if _, _, tts := a.providers(); tts != nil {
			a.say(ctx, c, ag.Greeting, in)
		}
	}

	a.vadLoop(ctx, c, in, func(seg []int16) (bool, []int16) {
		return a.round(ctx, c, seg, in)
	})
	return nil
}

// vadLoop 负责断句：把持续音频切成一句话，交给 onUtterance 处理。
// onUtterance 返回 barge=true 时，pre 里的预录音频会并入下一句。
func (a *Agent) vadLoop(ctx context.Context, c *audiosock.Conn, in <-chan frame,
	onUtterance func(seg []int16) (barge bool, pre []int16)) {

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

	a.vadLoop(ctx, c, in, func(seg []int16) (bool, []int16) {
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
			if bargeIn {
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
func (a *Agent) round(ctx context.Context, c *audiosock.Conn, seg []int16, in <-chan frame) (bool, []int16) {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	respCh := make(chan *reply, 1)
	go func() {
		respCh <- a.respond(cctx, seg)
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

// readLoop 把 AudioSocket 帧转成 PCM / DTMF 事件。
func (a *Agent) readLoop(ctx context.Context, c *audiosock.Conn, in chan<- frame) {
	defer close(in)
	for {
		fr, err := c.ReadFrame()
		if err != nil {
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
			select {
			case in <- frame{dtmf: string(fr.Payload)}:
			case <-ctx.Done():
				return
			}
		case audiosock.TypeHangup:
			return
		}
	}
}

// respond 执行 ASR → 对话 → TTS，返回 8k PCM。
func (a *Agent) respond(ctx context.Context, pcm8 []int16) *reply {
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

	answer, err := chat.Chat(actx, a.Cfg.AI.System, text)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		log.Printf("[voice] 对话失败: %v", err)
		a.reportErr("chat", err)
		return nil
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
func (a *Agent) say(ctx context.Context, c *audiosock.Conn, text string, in <-chan frame) {
	_, _, tts := a.providers()
	if tts == nil {
		return
	}
	actx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	audio, err := tts.Speak(actx, text)
	if err != nil {
		log.Printf("[voice] 开场白 TTS 失败: %v", err)
		return
	}
	pcm, rate, err := media.DecodeWAV(audio)
	if err != nil {
		log.Printf("[voice] 开场白音频解析失败: %v", err)
		return
	}
	if rate != sampleRate {
		pcm = media.Resample(pcm, rate, sampleRate)
	}
	a.play(ctx, c, pcm, in)
}

// play 按 20ms 节奏播放 PCM，同时检测打断与 DTMF。
func (a *Agent) play(ctx context.Context, c *audiosock.Conn, pcm8 []int16, in <-chan frame) (bool, []int16) {
	const chunk = frameBytes / 2
	ticker := time.NewTicker(frameMS * time.Millisecond)
	defer ticker.Stop()

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
