// Package worker 执行会话里的异步任务：主动发传真、主动回拨。
//
// 任务来源：
//   - AI 在对话里识别到“帮我发传真给 XXX”，通话结束时挂到会话上；
//   - 传真到达后需要通知/回拨。
//
// 执行策略：
//   - fax：调用 fax.Send 走现有传真链路（含提示音、DTMF、归档、邮件）；
//   - callback：按配置的回拨策略决定是否直接拨号，拨不通就发通知。
package worker

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"sipvoice/internal/config"
	"sipvoice/internal/fax"
	"sipvoice/internal/session"
)

// Worker 是后台任务执行器。
type Worker struct {
	Sessions *session.Manager
	// Originate 由上层注入：拨号到某个分机并执行指定拨号方案上下文。
	Originate func(ctx context.Context, ext, context, exten string) error
	// Notify 发通知（失败/完成时用）。
	Notify func(ctx context.Context, title, body string) error

	mu    sync.Mutex
	busy  map[string]bool // 正在执行的任务 id
	faxMu sync.Mutex      // 传真发送串行锁（Asterisk 侧不能并发）
}

// New 创建 worker。
func New(sm *session.Manager) *Worker {
	return &Worker{Sessions: sm, busy: map[string]bool{}}
}

// Run 阻塞轮询待办任务，直到 ctx 取消。
func (w *Worker) Run(ctx context.Context) {
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			w.sweep(ctx)
		}
	}
}

// sweep 扫一遍所有会话的待办任务并执行。
func (w *Worker) sweep(ctx context.Context) {
	if w.Sessions == nil {
		return
	}
	for _, item := range w.Sessions.PendingTasks() {
		key, task := item.Key, item.Task
		// 带延迟的任务（如传真回拨）到点才执行
		if !taskReady(task.CreatedAt, task.Params) {
			continue
		}
		w.mu.Lock()
		if w.busy[task.ID] {
			w.mu.Unlock()
			continue
		}
		w.busy[task.ID] = true
		w.mu.Unlock()

		go func() {
			defer func() {
				w.mu.Lock()
				delete(w.busy, task.ID)
				w.mu.Unlock()
			}()
			w.exec(ctx, key, task)
		}()
	}
}

// exec 执行单个任务。
func (w *Worker) exec(ctx context.Context, key string, t session.Task) {
	c := config.Get()
	switch t.Type {
	case "fax":
		w.doSendFax(ctx, key, t)
	case "callback":
		w.doCallback(ctx, key, t, c)
	default:
		log.Printf("[worker] 未知任务类型 %q，跳过", t.Type)
		w.Sessions.FinishTask(key, t.ID, true, "未知任务类型")
	}
}

// doSendFax 主动把一份存档传真发到会话所属号码。
func (w *Worker) doSendFax(ctx context.Context, key string, t session.Task) {
	w.faxMu.Lock()
	defer w.faxMu.Unlock()
	file, _ := t.Params["file"].(string)
	text, _ := t.Params["text"].(string)
	dest, _ := t.Params["dest"].(string)
	if dest == "" {
		dest = key
	}
	// 有 text 就先渲染成传真（AI 回复/代码转 PDF 再转 TIFF）
	if file == "" && text != "" {
		log.Printf("[worker] 用文本生成传真 → %s（%d 字）", dest, len([]rune(text)))
		res := fax.TextToFaxAndSend(text, dest)
		w.finishFax(ctx, key, t, dest, res)
		return
	}
	if file == "" {
		log.Printf("[worker] 发传真任务缺少文件，跳过（号码 %s）", dest)
		w.Sessions.FinishTask(key, t.ID, true, "缺少传真文件")
		return
	}
	log.Printf("[worker] 开始主动发传真 → %s（文件 %s）", dest, file)
	cfg := fax.Load()
	res := fax.Send(cfg, file, dest)
	if res.OK {
		log.Printf("[worker] 传真发送成功 → %s：%s", dest, res.Message)
		w.Sessions.Append(key, session.RoleAssistant,
			fmt.Sprintf("（系统提示：传真已发送到 %s）", dest), "fax")
		w.Sessions.FinishTask(key, t.ID, false, "")
		return
	}
	log.Printf("[worker] 传真发送失败 → %s：%s", dest, res.Message)
	w.Sessions.Append(key, session.RoleAssistant,
		fmt.Sprintf("（系统提示：传真发送失败——%s）", res.Message), "fax")
	w.Sessions.FinishTask(key, t.ID, true, res.Message)

	// 失败时发通知（只在最后一次尝试后发，避免刷屏）
	maxAttempts := 2
	if t.Attempts+1 >= maxAttempts {
		if w.Notify != nil {
			_ = w.Notify(ctx, "SIPVoice 传真发送失败",
				fmt.Sprintf("目标 %s\n原因：%s\n\n已放弃重试。", dest, res.Message))
		}
		return
	}
	// 还有重试机会：任务继承已尝试次数，源文件被移走时改用归档路径
	params := map[string]any{}
	for k, v := range t.Params {
		params[k] = v
	}
	if res.Archived != "" {
		params["file"] = res.Archived
	}
	log.Printf("[worker] 传真失败，安排第 %d 次重试（用 %v）", t.Attempts+2, params["file"])
	w.Sessions.AddTask(key, session.Task{
		ID:       t.ID + "-r" + fmt.Sprint(t.Attempts+1),
		Type:     "fax",
		Attempts: t.Attempts + 1,
		Params:   params,
	})
}

// doCallback 按回拨策略决定是否主动拨打用户分机。
func (w *Worker) doCallback(ctx context.Context, key string, t session.Task, c *config.Config) {
	policy := c.Session.CallbackPolicy
	msg, _ := t.Params["message"].(string)
	if msg == "" {
		msg = "我这里有一件事想跟您确认一下。"
	}

	switch policy {
	case "never":
		log.Printf("[worker] 回拨策略为 never，跳过（号码 %s）", key)
		w.Sessions.FinishTask(key, t.ID, false, "策略为 never")
		return
	case "notify":
		// 只发通知，不自动拨
		if w.Notify != nil {
			if err := w.Notify(ctx, "SIPVoice 有话想跟您说",
				fmt.Sprintf("AI 想回拨到 %s：%s\n\n请回拨 9000 继续。", key, msg)); err != nil {
				log.Printf("[worker] 回拨通知失败: %v", err)
			}
		}
		log.Printf("[worker] 回拨策略为 notify，已发通知（号码 %s）", key)
		w.Sessions.FinishTask(key, t.ID, false, "")
		return
	}

	// 默认 ask：直接拨一次，拨不通再发通知
	if w.Originate == nil {
		log.Printf("[worker] 未配置拨号能力，改为发通知（号码 %s）", key)
		if w.Notify != nil {
			_ = w.Notify(ctx, "SIPVoice 有话想跟您说",
				fmt.Sprintf("AI 想回拨到 %s：%s", key, msg))
		}
		w.Sessions.FinishTask(key, t.ID, false, "未配置拨号能力")
		return
	}

	log.Printf("[worker] 主动回拨 → %s", key)
	if err := w.Originate(ctx, key, "fax-callback", "reply"); err != nil {
		log.Printf("[worker] 回拨失败: %v", err)
		w.Sessions.FinishTask(key, t.ID, true, err.Error())
		w.Sessions.EndCall(key)
		if w.Notify != nil {
			_ = w.Notify(ctx, "SIPVoice 回拨失败",
				fmt.Sprintf("尝试回拨 %s 失败：%v\n\n请回拨 9000 继续。", key, err))
		}
		return
	}
	log.Printf("[worker] 回拨完成 → %s", key)
	w.Sessions.FinishTask(key, t.ID, false, "")
	w.Sessions.EndCall(key) // 复位为 idle，避免卡在 in_call
}

// finishFax 统一处理发传真的收尾（写会话、通知、重试）。
func (w *Worker) finishFax(ctx context.Context, key string, t session.Task, dest string, res fax.SendResult) {
	// 上一份还在发：不算失败，原样排队等下一轮（不消耗重试次数）
	if res.Busy {
		log.Printf("[worker] 传真机忙，任务排队等待 → %s", dest)
		time.Sleep(3 * time.Second)
		w.Sessions.AddTask(key, session.Task{
			ID:       t.ID + "-q" + fmt.Sprint(time.Now().UnixNano()),
			Type:     "fax",
			Attempts: t.Attempts,
			Params:   t.Params,
		})
		return
	}
	if res.OK {
		log.Printf("[worker] 传真发送成功 → %s：%s", dest, res.Message)
		w.Sessions.Append(key, session.RoleAssistant,
			fmt.Sprintf("（系统提示：传真已发送到 %s）", dest), "fax")
		w.Sessions.FinishTask(key, t.ID, false, "")
		return
	}
	log.Printf("[worker] 传真发送失败 → %s：%s", dest, res.Message)
	w.Sessions.Append(key, session.RoleAssistant,
		fmt.Sprintf("（系统提示：传真发送失败——%s）", res.Message), "fax")
	w.Sessions.FinishTask(key, t.ID, true, res.Message)

	maxAttempts := 2
	if t.Attempts+1 >= maxAttempts {
		if w.Notify != nil {
			_ = w.Notify(ctx, "SIPVoice 传真发送失败",
				fmt.Sprintf("目标 %s\n原因：%s\n\n已放弃重试。", dest, res.Message))
		}
		return
	}
	params := map[string]any{}
	for k, v := range t.Params {
		params[k] = v
	}
	if res.Archived != "" {
		params["file"] = res.Archived
		delete(params, "text")
	}
	log.Printf("[worker] 传真失败，安排第 %d 次重试", t.Attempts+2)
	w.Sessions.AddTask(key, session.Task{
		ID:       t.ID + "-r" + fmt.Sprint(t.Attempts+1),
		Type:     "fax",
		Attempts: t.Attempts + 1,
		Params:   params,
	})
}
