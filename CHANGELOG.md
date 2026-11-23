# 变更记录

本项目从"电话能打通"一路做到"可发布"，版本号按 **主.次.修订 b构建** 递增。

## v1.3.1 — 打包与分发

- 新增 `.deb` 安装包（amd64 / arm64）：`scripts/build-deb.sh`，dpkg 管理依赖与卸载，postinst 负责建用户、部署 Asterisk 配置、启动服务
- 新增 Docker 镜像（Asterisk + voicebridge 一体）：`Dockerfile` + `docker/entrypoint.sh`，实测容器内完整通话链路可用
- 发布流程改为 tag 触发，自动附带 tar.gz 与 deb
- `.deb` 的 purge 会清理 /etc/sipvoice 与用户；remove 保留配置

## v1.3.0 — 部署收尾

- 新增发布包与一键部署：`install.sh`（幂等）、`upgrade.sh`（失败自动回滚）、`uninstall.sh`
- `install.sh` 首次安装约 43 秒，重复执行 3~4 秒
- 从零验证中发现并修复：
  - `/etc/asterisk` 被删后 dpkg 不补 conffiles → 自动 `--reinstall asterisk-config`
  - Asterisk 22 启动后 PJSIP 端点偶发为空 → systemd drop-in 自动 `pjsip reload`
- 支持 linux/amd64 与 linux/arm64 交叉编译

## v1.2.0 — 端到端语音与告警

- 接入阿里云百炼 **Qwen-Omni-Realtime**（WebSocket，Manual 模式配合自研 VAD/打断）
  - 实测建会话 ~230ms，首包 520~790ms，18 秒回复在 3.9 秒内流完
- 自研 WebSocket 客户端（`internal/ws`）与 IPv4 优先拨号（`internal/netutil`），保持零第三方依赖
- ServerChan 告警推送（启动 / 云端失败 / 磁盘 >90%，同类 5 分钟去重）

## v1.1.0 — 服务商适配与在线测试

- 11 家服务商预设；对话 / ASR / TTS 可各选各的服务商
- 在线拉取模型列表（OpenAI 兼容 `/models`、Ollama `/api/tags`），模型框可输入可下拉
- 在线测试：TTS 合成试听、ASR 上传或录音识别（ffmpeg 转 16k）
- 修复云端首包慢：共享连接池 + 每 60 秒保活，面板 ASR 20s → 0.9s

## v1.0.1 — 控制台修复

- 修复前端脚本被 shell 转义破坏导致的"面板像是断连"
- 话机列表只显示 IP，不再显示整串 SIP URI

## v1.0.0 — 电话体验补齐

- 处理中提示音（频率/间隔/时长/音量可调）
- DTMF 控制：`0` 打断、`9` 挂断，按键可配
- 话机台账：增删改、注册状态、配置模板下载、互播策略、摘机直通，一键生成 Asterisk 配置并 reload
- 长回复默认不截断

## v0.9.0 — 云端语音

- ASR 接入硅基流动 SenseVoice，TTS 接入 CosyVoice2（OpenAI 兼容）
- 用真人录音做 CER 实测：SenseVoice 1.6~7.4%（0.7~1.2s）

## v0.8.0 — 安全与账号

- 控制台 HTTPS（自签 ECDSA，可在面板导入自有证书）
- 账号管理：新增/删除/改名/改密，PBKDF2 存储

## v0.7.0 — 鉴权

- 控制台口令认证与会话 Cookie，首次部署强制创建管理员
- 面板改为监听 `0.0.0.0:8080`，局域网可直接访问

## v0.6.0 — 控制台成型

- 五个标签页：概览 / 模型 / AI / 语音 / 系统
- 服务器探针、本地 whisper 模型管理（下载/上传/启用/删除）

## v0.5.0 — 并发与稳定性

- 并发验证与容量边界；修复 SIGTERM 杀不掉的问题

## v0.4.x — 真实链路

- 接入 DeepSeek，本地 whisper.cpp，edge-tts；简体中文提示词
- 修复 systemd `ProtectSystem` 导致配置写不进去

## v0.3.0 — 语音链路成型

- AudioSocket 媒体桥、VAD 断句、打断、ASR→LLM→TTS 回合
- 内嵌 WebUI、systemd 服务与一键安装

## v0.2.0 — Asterisk 接入

- Asterisk 22 安装与 PJSIP 配置（1001/1002、9000 AI、600 传真）
- AudioSocket 协议与自研 calltest

## v0.1.0 — 信令闭环

- SIP/OPTIONS/REGISTER/INVITE 与 RTP、G.711 编解码
- OpenAI 兼容的 ASR/TTS/Chat 适配器骨架
