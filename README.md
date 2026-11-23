# SIPVoice · 轻量 SIP 语音 AI 网关

![Go](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go&logoColor=white)
![Platform](https://img.shields.io/badge/platform-linux%2Famd64%20%7C%20linux%2Farm64-lightgrey)
![License](https://img.shields.io/badge/license-MIT-green)

把一通普通的电话，变成能对话的 AI 助手：**话机/网关 → Asterisk → voicebridge → 任意大模型**。
无公网、无云控制台、一条命令装完，跑在一台 2 核小机器上。

> Turn ordinary phone calls into an AI conversation. Asterisk + a single Go binary,
> 100% self-hosted, one-command install. Work in progress, MIT licensed.

---

## 它解决什么问题

- 家里/办公室的**普通话机、传真机、实体语音网关**，想接上一个 AI 助手
- 不想把设备注册到厂商云、不想开公网端口、不想付平台月费
- 希望"装好就能拨号说话"，而不是先学一套 SIP 运维

## 能做什么

- ☎️ **电话呼入 AI 对话**：拨 `9000`，AI 接听、断句、可打断、可换音色
- 🎙️ **两种语音链路**
  - 三段式：云端/本地 ASR → 对话模型 → TTS（可各用各的服务商）
  - 端到端：Qwen-Omni-Realtime，音频直接进模型、边生成边回放，**首包约 0.5 秒**
- 🖥️ **Web 控制台**：服务商预设、在线拉取模型列表、TTS 试听、ASR 上传/录音识别、话机台账、监控
- 🔔 **告警推送**：ServerChan 转发到微信（启动 / 云端失败 / 磁盘告警）
- 📠 传真接收（T.38/G.711，落 TIFF）— 尚未做 PDF 转换
- 🧰 部署：`install.sh` 一键安装、`upgrade.sh` 带自动回滚、`uninstall.sh`

## 架构

```
 ┌──────────┐   SIP/RTP    ┌───────────┐   AudioSocket   ┌──────────────┐
 │ 话机 /   │ ───────────► │ Asterisk  │ ──────────────► │ voicebridge  │
 │ 语音网关 │ ◄─────────── │   22      │ ◄────────────── │  (Go, 单文件) │
 └──────────┘              └───────────┘   PCMU 20ms     └──────┬───────┘
                                                                 │ HTTPS / WSS
                                                        ┌────────▼────────┐
                                                        │ ASR / LLM / TTS │
                                                        │ 或 端到端语音模型 │
                                                        └─────────────────┘
```

- Asterisk 负责 SIP 信令、G.711 编解码、传真
- voicebridge 负责 VAD 断句、打断（barge-in）、DTMF、AI 调用、Web 控制台
- 音频通过 AudioSocket（TCP）在两者之间传递，不依赖外部服务

## 快速开始

前置：Ubuntu 22.04/24.04/26.04（Debian 亦可）、2 核 2 GB、能访问你选的 AI 服务商。

```bash
# 1. 拿到发布包（或自行编译，见下）
scp sipvoice-<版本>-linux-amd64.tar.gz user@主机:/tmp/
ssh user@主机
tar -xzf /tmp/sipvoice-<版本>-linux-amd64.tar.gz
cd sipvoice-<版本>

# 2. 一条命令安装（会自动装 asterisk / ffmpeg / curl）
sudo bash install.sh

# 3. 打开控制台，创建管理员，填服务商与 Key
#    https://<主机IP>:8080
```

装完拨号：

| 号码 | 用途 |
|---|---|
| `9000` | 与 AI 对话 |
| `600` | 接收传真 |
| `1001` / `1002` | 测试分机（密码 `SipVoice1001` / `SipVoice1002`） |
| 分机互拨 | 1001 ↔ 1002 |

> 首次登录后请立刻改掉默认分机密码：控制台 →「话机」→ 编辑。

## 截图

| AI 服务与模型 | 语音与在线测试 |
|---|---|
| ![AI](docs/screenshots/panel-ai.png) | ![Voice](docs/screenshots/panel-voice.png) |

## 配置

全部配置都在控制台里改，保存即生效（写入 `/etc/sipvoice/config.json`）：

- **服务商**：DeepSeek / 硅基流动 / 阿里百炼 / OpenAI / Groq / 智谱 / Kimi / 混元 / 火山方舟 / Ollama / 自定义
- **模型**：可手填，也可「拉取列表」用你的 Key 在线获取真实可用模型
- **端到端语音**：`realtime.enabled`，默认 `qwen3.5-omni-flash-realtime`
- **对话行为**：静音判停、能量阈值、最大句长、是否允许打断、处理提示音
- **按键**：`0` 打断播报，`9` 挂断（可改）

配置文件也可以直接编辑（重启服务生效）：

```json
{
  "ai":       { "base_url": "https://api.deepseek.com/v1", "chat_model": "deepseek-flash" },
  "realtime": { "enabled": true, "model": "qwen3.5-omni-flash-realtime" },
  "agent":    { "greeting": "你好，有什么能够帮助你的？" }
}
```

## 部署与运维

```bash
sudo bash install.sh                 # 幂等安装（首次约 40 秒）
sudo bash upgrade.sh <新二进制>       # 升级，健康检查失败自动回滚
sudo bash upgrade.sh --rollback       # 回滚
sudo bash uninstall.sh [--purge]      # 卸载
systemctl status sipvoice             # 服务状态
journalctl -u sipvoice -f             # 实时日志
```

## 从源码编译

需要 Go 1.24+：

```bash
git clone https://github.com/USER/sipvoice.git && cd sipvoice
make build          # 本机
make release        # linux/amd64 + linux/arm64 发布包
go test ./...       # 单元测试
```

交叉编译（无需目标机）：

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o dist/voicebridge-linux-arm64 ./cmd/voicebridge
```

## 目录结构

```
cmd/voicebridge/    程序入口（server / agent / calltest / doctor / mockai 等模式）
internal/
  audiosock/        AudioSocket 协议与连接
  voice/            VAD 断句、打断、回合编排
  omni/             端到端语音会话（Qwen-Omni-Realtime）
  ws/               自研 WebSocket 客户端（零第三方依赖）
  adapters/         云端 ASR/TTS/对话、本地 whisper、模型列表
  webui/            内嵌控制台与 REST API
  phones/           话机台账，生成 Asterisk 配置
  auth/ config/ models/ media/ netutil/ sysinfo/ ...
deploy/             安装/升级/卸载脚本与 Asterisk 基线配置
```

## 已知限制

- 传真只完成"接收并落 TIFF"，PDF 转换和面板下载未做
- 端到端语音目前适配阿里云百炼 Qwen-Omni-Realtime（协议兼容者可自行扩展）
- 未做 Kubernetes / 容器编排，定位是单机小盒子
- 默认分机密码是公开的，务必在控制台改掉

## 路线图

- [x] SIP 信令、AudioSocket 媒体桥、VAD/打断
- [x] 三段式与端到端语音、Web 控制台、一键部署
- [x] 服务商预设、在线模型列表、TTS/ASR 在线测试、告警推送
- [ ] 传真：TIFF → PDF + 控制台查看/下载
- [ ] Docker 镜像
- [ ] 多语言 README

## 许可

[MIT](LICENSE)
