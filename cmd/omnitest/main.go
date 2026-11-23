// omnitest 用来验证端到端语音模型：读一段 wav，走一次 realtime 回合并打印耗时。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"sipvoice/internal/media"
	"sipvoice/internal/omni"
)

func main() {
	wavPath := flag.String("wav", "/tmp/bench16.wav", "输入 wav（任意采样率）")
	keyFile := flag.String("keyfile", "/tmp/bl.key", "API Key 文件")
	model := flag.String("model", "qwen3.5-omni-flash-realtime", "模型名")
	voice := flag.String("voice", "Tina", "音色")
	flag.Parse()

	key, _ := os.ReadFile(*keyFile)
	k := strings.TrimSpace(string(key))
	if k == "" {
		fmt.Println("缺少 API Key")
		os.Exit(1)
	}
	raw, err := os.ReadFile(*wavPath)
	if err != nil {
		fmt.Println("读取 wav:", err)
		os.Exit(1)
	}
	pcm, rate, err := media.DecodeWAV(raw)
	if err != nil {
		fmt.Println("解析 wav:", err)
		os.Exit(1)
	}
	if rate != 8000 {
		pcm = media.Resample(pcm, rate, 8000)
	}
	fmt.Printf("输入 %.1fs @8k\n", float64(len(pcm))/8000)

	c := &omni.Client{Cfg: omni.Config{
		APIKey:       k,
		Model:        *model,
		Voice:        *voice,
		Instructions: "你是一名电话语音助手，回答简短口语化。",
	}}
	ctx := context.Background()

	t0 := time.Now()
	s, err := c.Open(ctx)
	if err != nil {
		fmt.Println("建立会话失败:", err)
		os.Exit(1)
	}
	defer s.Close()
	fmt.Printf("会话建立 %s（模型 %s）\n", time.Since(t0).Round(time.Millisecond), *model)

	t1 := time.Now()
	turn, err := s.Ask(ctx, pcm)
	if err != nil {
		fmt.Println("发送失败:", err)
		os.Exit(1)
	}
	total := 0
	first := time.Duration(0)
	deadline := time.After(60 * time.Second)
loop:
	for {
		select {
		case chunk := <-turn.Audio:
			if chunk == nil {
				break loop
			}
			if total == 0 {
				first = time.Since(t1)
			}
			total += len(chunk)
		case <-deadline:
			break loop
		}
	}
	user, reply, err := turn.Wait()
	fmt.Printf("用户: %s\n", user)
	fmt.Printf("助手: %s\n", reply)
	fmt.Printf("首包 %s，音频 %.1fs，总耗时 %s，err=%v\n",
		first.Round(time.Millisecond), float64(total)/8000, time.Since(t1).Round(time.Millisecond), err)
}
