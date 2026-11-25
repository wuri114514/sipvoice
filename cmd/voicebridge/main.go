// sipvoice: SIP 语音网关 —— 电话终端 ⇄ ASR/TTS ⇄ AI 模型
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"sipvoice/internal/adapters"
	"sipvoice/internal/aibuild"
	"sipvoice/internal/audiosock"
	"sipvoice/internal/auth"
	"sipvoice/internal/calltest"
	"sipvoice/internal/config"
	"sipvoice/internal/mockai"
	"sipvoice/internal/models"
	"sipvoice/internal/notify"
	"sipvoice/internal/omni"
	"sipvoice/internal/phones"
	"sipvoice/internal/pipeline"
	"sipvoice/internal/session"
	"sipvoice/internal/sip"
	"sipvoice/internal/voice"
	"sipvoice/internal/webui"
	"sipvoice/internal/worker"
)

var version = "2.0.3"

func main() {
	var (
		mode    = flag.String("mode", "server", "运行模式: server | client | audio | calltest | agent | mockai | doctor")
		listen  = flag.String("listen", ":5060", "SIP 监听地址（server 模式）")
		target  = flag.String("target", "127.0.0.1:5060", "SIP 探测目标（client 模式）")
		rtpPort = flag.Int("rtp", 40000, "RTP 起始端口")
		aiURL   = flag.String("ai-url", "", "OpenAI 兼容 API base（如 https://api.openai.com/v1）")
		aiKey   = flag.String("ai-key", "", "API Key，默认读 SIPVOICE_API_KEY")
		chatMod = flag.String("chat-model", "gpt-4o-mini", "对话模型")
		asrMod  = flag.String("asr-model", "whisper-1", "语音识别模型")
		ttsMod  = flag.String("tts-model", "tts-1", "语音合成模型")
		system  = flag.String("system", "你是一名电话客服，回答要简短口语化，控制在两句话内。", "系统提示词")
		showVer = flag.Bool("version", false, "打印版本")

		audioListen = flag.String("audio-listen", "127.0.0.1:9092", "AudioSocket 监听地址（audio 模式）")
		audioMode   = flag.String("audio-mode", "echo", "audio 模式行为: echo | tone | wav")
		wavFile     = flag.String("wav", "", "audio-mode=wav 时循环播放的 8k/16bit/mono WAV")
		recFile     = flag.String("record", "", "audio-mode=wav 时上行录音输出路径")

		callNumber = flag.String("call-number", "9000", "calltest 被叫号码")
		callDur    = flag.Duration("call-duration", 5*time.Second, "calltest 通话时长")

		cfgPath    = flag.String("config", "config.json", "配置文件路径（agent 模式）")
		mockListen = flag.String("mock-listen", "127.0.0.1:8090", "mock AI 服务监听地址（mockai 模式）")
		webAddr    = flag.String("web", "0.0.0.0:8080", "WebUI 监听地址（agent 模式）")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("sipvoice", version)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch *mode {
	case "server":
		runServer(ctx, *listen, *rtpPort, *aiURL, *aiKey, *chatMod, *asrMod, *ttsMod, *system)
	case "client":
		runClient(ctx, *target)
	case "audio":
		runAudio(ctx, *audioListen, *audioMode, *wavFile, *recFile)
	case "calltest":
		runCalltest(ctx, *target, *callNumber, *wavFile, *recFile, *callDur)
	case "agent":
		runAgent(ctx, *cfgPath, *webAddr)
	case "mockai":
		go func() {
			if err := mockai.Start(*mockListen); err != nil {
				fmt.Fprintln(os.Stderr, "mockai error:", err)
				os.Exit(1)
			}
		}()
		<-ctx.Done()
	case "doctor":
		runDoctor(*aiURL, *aiKey)
	default:
		fmt.Fprintf(os.Stderr, "未知模式: %s\n", *mode)
		os.Exit(2)
	}
}

func runServer(ctx context.Context, listen string, rtpPort int, aiURL, aiKey, chatMod, asrMod, ttsMod, system string) {
	if aiKey == "" {
		aiKey = os.Getenv("SIPVOICE_API_KEY")
	}

	bridge := &pipeline.Bridge{System: system}
	if aiURL != "" {
		bridge.ASR = adapters.New(aiURL, aiKey, asrMod)
		bridge.Chat = adapters.New(aiURL, aiKey, chatMod)
		bridge.TTS = &adapters.Speech{C: adapters.New(aiURL, aiKey, ttsMod), Voice: "alloy"}
	}

	srv := &sip.Server{
		Addr:    listen,
		RTPPort: rtpPort,
		OnInvite: func(from, to, sdp string) {
			fmt.Printf("[invite] from=%s to=%s media=%s\n", from, to, firstLine(sdp))
		},
	}

	fmt.Printf("sipvoice %s listening on %s (RTP from %d)\n", version, listen, rtpPort)
	if bridge.ASR == nil {
		fmt.Println("[warn] 未配置 -ai-url，当前仅做信令；媒体链路将在后续版本接入")
	}
	if err := srv.ListenAndServe(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "server error:", err)
		os.Exit(1)
	}
	fmt.Println("bye")
}

func runClient(ctx context.Context, target string) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	line, err := sip.Client(cctx, target)
	if err != nil {
		fmt.Fprintln(os.Stderr, "OPTIONS 失败:", err)
		os.Exit(1)
	}
	fmt.Println("OPTIONS ->", line)
}

func runAudio(ctx context.Context, listen, mode, wav, record string) {
	var h audiosock.Handler
	switch mode {
	case "echo":
		h = audiosock.Echo
	case "tone":
		h = audiosock.Tone
	case "wav":
		if wav == "" {
			fmt.Fprintln(os.Stderr, "-audio-mode=wav 需要 -wav 指定文件")
			os.Exit(2)
		}
		h = func(ctx context.Context, c *audiosock.Conn) error {
			return audiosock.WAV(ctx, c, wav, record)
		}
	default:
		fmt.Fprintf(os.Stderr, "未知 audio-mode: %s\n", mode)
		os.Exit(2)
	}

	log.Printf("sipvoice %s AudioSocket 监听 %s，模式 %s", version, listen, mode)
	if err := audiosock.Serve(ctx, listen, h); err != nil {
		fmt.Fprintln(os.Stderr, "audio error:", err)
		os.Exit(1)
	}
}

func runAgent(ctx context.Context, cfgPath, webAddr string) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "配置加载失败:", err)
		os.Exit(1)
	}

	ag := &voice.Agent{Cfg: cfg}

	// nt 在下面创建；这里先用指针占位，配置保存后热更新。
	var nt *notify.ServerChan

	// buildProviders 依据配置创建 AI 适配器；WebUI 保存后也会调用它热更新。
	buildProviders := func(c *config.Config) {
		p := aibuild.Build(c)
		ag.SetProviders(p.ASR, p.Chat, p.TTS)

		if c.Realtime.Enabled && c.Realtime.APIKey != "" {
			ag.SetOmni(&omni.Client{Cfg: omni.Config{
				BaseURL:      c.Realtime.BaseURL,
				APIKey:       c.Realtime.APIKey,
				Model:        c.Realtime.Model,
				Voice:        c.Realtime.Voice,
				Instructions: c.Realtime.Instructions,
			}})
			log.Printf("[voice] 已启用端到端语音模型 %s（音色 %s）", c.Realtime.Model, c.Realtime.Voice)
		} else {
			ag.SetOmni(nil)
		}
		if nt != nil {
			nt.SetKey(c.Notify.ServerChanKey, time.Duration(c.Notify.MinIntervalMS)*time.Millisecond)
		}
	}
	buildProviders(cfg)

	// 后台保活：定期访问各家 /models，保持 DNS/TLS 连接温热，避免每通电话首包等十几秒。
	go func() {
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for {
			c := config.Get()
			done := map[string]bool{}
			for _, b := range []string{c.AI.BaseURL, c.AI.ASRBaseURL, c.AI.TTSBaseURL} {
				if b == "" || done[b] {
					continue
				}
				done[b] = true
				adapters.Warm(ctx, b, c.AI.APIKey)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	// 异步会话：按主叫号码保留对话上下文
	sessMgr := session.New(time.Duration(cfg.Session.TTLMinutes)*time.Minute, cfg.Session.MaxMessages)
	ag.Sessions = sessMgr

	// 异步任务 worker：执行 AI 识别的发传真 / 回拨
	faxWorker := worker.New(sessMgr)
	faxWorker.Originate = func(wctx context.Context, ext, ctxName, exten string) error {
		// 分机走 PJSIP，拨号方案扩展号走 Local（与 fax.originateCmd 同一策略）
		ch := "PJSIP/" + ext
		if o, err := runAsterisk("pjsip show endpoint %s", ext); err != nil || !strings.Contains(o, "Endpoint:") {
			ch = "Local/" + ext + "@internal"
		}
		_, err := runAsterisk("channel originate %s extension %s@%s", ch, exten, ctxName)
		return err
	}
	faxWorker.Notify = func(nctx context.Context, title, body string) error {
		push(nctx, nt, title, body)
		return nil
	}
	go faxWorker.Run(ctx)

	// 传真到达自动分析：新传真交给视觉模型，结果写进会话与传真记录
	startFaxWatcher(ctx, sessMgr, func(wctx context.Context, item string) (string, error) {
		return analyzeFaxItem(wctx, item)
	}, func(key, summary string) {
		faxWorker.NotifyFaxArrived(ctx, key, summary)
	})

	store := webui.NewStore()
	ag.Obs = store

	// 通知通道：ServerChan + 邮局（配置保存后热更新）
	nt = notify.New(cfg.Notify.ServerChanKey, time.Duration(cfg.Notify.MinIntervalMS)*time.Millisecond)
	ag.SetOnError(func(kind, msg string) {
		c := config.Get()
		if !c.Notify.Enabled || !c.Notify.OnError {
			return
		}
		title := "SIPVoice 调用失败（" + kind + "）"
		body := msg + "\n\n时间：" + time.Now().Format("2006-01-02 15:04:05")
		push(context.Background(), nt, title, body)
	})
	startMonitors(ctx, nt)

	modelDir := os.Getenv("SIPVOICE_MODELS_DIR")
	if modelDir == "" {
		modelDir = "/opt/sipvoice/models"
	}
	if _, err := os.Stat(modelDir); err != nil {
		modelDir = "./models"
	}
	mm := models.New(modelDir)

	usersPath := os.Getenv("SIPVOICE_USERS")
	if usersPath == "" {
		usersPath = "/etc/sipvoice/users.json"
	}
	as, err := auth.New(usersPath)
	if err != nil {
		log.Printf("[auth] 账号文件加载失败: %v", err)
	}

	tlsDir := os.Getenv("SIPVOICE_TLS_DIR")
	if tlsDir == "" {
		tlsDir = "/etc/sipvoice/tls"
	}
	tl := webui.NewTLManager(tlsDir)

	phonesPath := os.Getenv("SIPVOICE_PHONES")
	if phonesPath == "" {
		phonesPath = "/etc/sipvoice/phones.json"
	}
	ps := phones.New(phonesPath)
	pj := os.Getenv("SIPVOICE_PJSIP_CONF")
	if pj == "" {
		pj = "/etc/asterisk/pjsip.conf"
	}
	ex := os.Getenv("SIPVOICE_EXTENSIONS_CONF")
	if ex == "" {
		ex = "/etc/asterisk/extensions.conf"
	}

	webui.Start(webui.Options{
		Addr:           webAddr,
		Version:        version,
		Agent:          ag,
		Store:          store,
		Models:         mm,
		Auth:           as,
		TL:             tl,
		Phones:         ps,
		Sessions:       sessMgr,
		PJSIPConf:      pj,
		ExtensionsConf: ex,
		BackupDir:      "/etc/sipvoice/backups",
		OnConfig:       buildProviders,
	})

	aiDesc := cfg.AI.BaseURL
	if aiDesc == "" {
		aiDesc = "未配置（仅 VAD/媒体）"
	}
	log.Printf("sipvoice %s agent 监听 %s，AI=%s，面板 http://%s", version, cfg.Audio.Listen, aiDesc, webAddr)
	if err := audiosock.Serve(ctx, cfg.Audio.Listen, ag.Handle); err != nil {
		fmt.Fprintln(os.Stderr, "agent error:", err)
		os.Exit(1)
	}
}

func runCalltest(ctx context.Context, target, number, wav, record string, dur time.Duration) {
	if err := calltest.Run(ctx, calltest.Config{
		Target:   target,
		Number:   number,
		WAVIn:    wav,
		WAVOut:   record,
		Duration: dur,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "calltest 失败:", err)
		os.Exit(1)
	}
	fmt.Println("calltest OK")
}

func runDoctor(aiURL, aiKey string) {
	fmt.Println("sipvoice doctor")
	fmt.Println("  version :", version)
	if aiURL == "" {
		fmt.Println("  ai      : 未配置（仅信令模式）")
		return
	}
	c := adapters.New(aiURL, aiKey, "gpt-4o-mini")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	reply, err := c.Chat(ctx, "只回答收到", "ping")
	if err != nil {
		fmt.Println("  ai      : 不可用 ->", err)
		return
	}
	fmt.Println("  ai      : 可用 ->", reply)
}

func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}

// runAsterisk 执行一条 Asterisk CLI 命令（带 sudo 兜底）。
func runAsterisk(format string, args ...any) (string, error) {
	cmdline := fmt.Sprintf(format, args...)
	for _, prog := range [][]string{
		{"sudo", "-n", "/usr/sbin/asterisk", "-rx", cmdline},
		{"/usr/sbin/asterisk", "-rx", cmdline},
	} {
		out, err := exec.Command(prog[0], prog[1:]...).CombinedOutput()
		if err == nil {
			return string(out), nil
		}
	}
	return "", fmt.Errorf("asterisk 命令失败: %s", cmdline)
}
