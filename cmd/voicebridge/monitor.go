// 把通知、磁盘巡检、端到端语音等运行时能力的装配集中在这里，保持 main.go 清爽。
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"sipvoice/internal/config"
	"sipvoice/internal/notify"
	"sipvoice/internal/sysinfo"
)

// startMonitors 启动后台巡检：磁盘、服务自检，命中时推送。
func startMonitors(ctx context.Context, nt *notify.ServerChan) {
	go func() {
		// 启动后先等 30 秒，让服务完全起来
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
		if c := config.Get(); c.Notify.Enabled && c.Notify.OnStartup {
			title := "SIPVoice 已启动"
			body := fmt.Sprintf("版本 %s\n时间 %s\n面板 https://%s\n实时语音：%v",
				version, time.Now().Format("2006-01-02 15:04:05"), "", c.Realtime.Enabled)
			push(ctx, nt, title, body)
		}

		tick := time.NewTicker(15 * time.Minute)
		defer tick.Stop()
		for {
			checkDisk(ctx, nt)
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
}

func checkDisk(ctx context.Context, nt *notify.ServerChan) {
	c := config.Get()
	if !c.Notify.Enabled || !c.Notify.OnDisk {
		return
	}
	s := sysinfo.Collect()
	if s.DiskTotal == 0 {
		return
	}
	used := 100.0 * float64(s.DiskTotal-s.DiskFree) / float64(s.DiskTotal)
	if used < 90 {
		return
	}
	body := fmt.Sprintf("根分区已用 %.0f%%\n剩余 %.2f GB / 共 %.2f GB\n请及时清理 /tmp、/var/log 或旧模型。",
		used, float64(s.DiskFree)/(1<<30), float64(s.DiskTotal)/(1<<30))
	push(ctx, nt, "SIPVoice 磁盘空间告警", body)
}

// push 按当前配置把消息发到所有启用的通道（ServerChan / 邮局）。
// 配置文件里的模板每次发送时重新读取，面板改完立即生效。
func push(ctx context.Context, sc *notify.ServerChan, title, body string) {
	c := config.Get()
	chans := &notify.Multi{}
	if c.Notify.Enabled && c.Notify.ServerChanKey != "" {
		sc.SetKey(c.Notify.ServerChanKey, time.Duration(c.Notify.MinIntervalMS)*time.Millisecond)
		chans.Channels = append(chans.Channels, sc)
	}
	if c.Notify.MailEnabled {
		mc := notify.NewMailChannel("/etc/sipvoice/mail.json",
			time.Duration(c.Notify.MinIntervalMS)*time.Millisecond)
		if c.Notify.MailSubject != "" {
			mc.Subject = c.Notify.MailSubject
		}
		if c.Notify.MailBody != "" {
			mc.Body = c.Notify.MailBody
		}
		chans.Channels = append(chans.Channels, mc)
	}
	if len(chans.Channels) == 0 {
		return
	}
	via, err := chans.SendOnce(ctx, title, body)
	if err != nil {
		log.Printf("[notify] %s 发送失败: %v", title, err)
		return
	}
	log.Printf("[notify] %s 已发送（%s）", title, via)
}
