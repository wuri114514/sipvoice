# SIPVoice · Lightweight SIP Voice AI Gateway

![Go](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go&logoColor=white)
![Platform](https://img.shields.io/badge/platform-linux%2Famd64%20%7C%20linux%2Farm64-lightgrey)
![Status](https://img.shields.io/badge/status-alpha-orange)
![License](https://img.shields.io/badge/license-MIT-green)

**English** · [简体中文](README.zh-CN.md)

Turn an ordinary phone call into a conversation with an AI assistant:
**phone / voice gateway → Asterisk → voicebridge → any LLM**.
No public IP, no vendor cloud, no monthly platform fee — a single Go binary that runs on a 2-core box.

> ⚠️ **Alpha software — please read before using**
>
> - This project was written **100% by AI** (an AI coding agent under human direction). It has been
>   tested on real hardware and real phone calls, but it has **not** been reviewed line by line by a
>   human maintainer.
> - It is at an **early stage** (v1.x): interfaces, configuration keys and deployment scripts may
>   change without notice.
> - **Do not use it in production**, in any life-safety context, or where a dropped call or a wrong
>   answer could cause harm. Evaluate it in a lab or at home first.
> - Provided **as is**, without warranty of any kind. You are responsible for the phone numbers,
>   credentials and AI usage costs you connect to it.

---

## What it does

- ☎️ **AI answers your phone**: dial `9000`, the AI picks up, listens, replies, can be interrupted
- 🎙️ **Two voice pipelines**
  - Pipeline: cloud/local ASR → chat model → TTS (each part can be a different provider)
  - End-to-end: Qwen-Omni-Realtime over WebSocket — audio in, audio out, **first response ≈ 0.5 s**
- 🖥️ **Web console**: provider presets, live model list, TTS preview, ASR upload/record test,
  phone extension inventory, system monitor
- 🔔 **Alerts**: ServerChan push to WeChat (startup / cloud errors / low disk)
- 📠 Fax receive (T.38 / G.711, saved as TIFF) — PDF conversion not implemented yet

## Architecture

```
 ┌────────────┐   SIP/RTP    ┌───────────┐   AudioSocket   ┌──────────────┐
 │ phone /    │ ───────────► │ Asterisk  │ ──────────────► │ voicebridge  │
 │ FXS gateway│ ◄─────────── │    22     │ ◄────────────── │ (Go, 1 file) │
 └────────────┘              └───────────┘   PCMU 20 ms    └──────┬───────┘
                                                                  │ HTTPS / WSS
                                                         ┌────────▼────────┐
                                                         │ ASR / LLM / TTS │
                                                         │ or end-to-end   │
                                                         └─────────────────┘
```

- **Asterisk** handles SIP signalling, G.711 codecs and fax
- **voicebridge** handles VAD segmentation, barge-in, DTMF, AI calls and the web console
- Audio flows over **AudioSocket** (TCP) between them — no external media service

## Requirements

- Ubuntu 22.04 / 24.04 / 26.04 or Debian 12+ (systemd)
- 2 CPU cores, 2 GB RAM, ~2 GB disk
- Network access to your chosen AI provider (and to apt for the dependencies)
- Optional: a SIP phone (MicroSIP, Zoiper, a real desk phone) or an FXS gateway

## Quick start

```bash
# 1. get a release package (or build it yourself, see below)
scp sipvoice-<version>-linux-amd64.tar.gz user@host:/tmp/
ssh user@host
tar -xzf /tmp/sipvoice-<version>-linux-amd64.tar.gz
cd sipvoice-<version>

# 2. one command install (installs asterisk / ffmpeg / curl for you)
sudo bash install.sh

# 3. open the console, create the admin account, add your provider key
#    https://<host-ip>:8080
```

Then dial:

| Number | Purpose |
|---|---|
| `9000` | Talk to the AI |
| `600` | Receive a fax |
| `1001` / `1002` | Test extensions (passwords `SipVoice1001` / `SipVoice1002`) |

> Change the default extension passwords right after the first login (Console → Phones).

## Screenshots

| AI providers | Voice & online tests |
|---|---|
| ![AI](docs/screenshots/panel-ai.png) | ![Voice](docs/screenshots/panel-voice.png) |

## Configuration

Everything is editable in the console; changes are written to `/etc/sipvoice/config.json` and applied
without a restart.

- **Providers**: DeepSeek, SiliconFlow, Alibaba DashScope, OpenAI, Groq, Zhipu, Moonshot, Tencent
  Hunyuan, Volcano Ark, local Ollama, custom OpenAI-compatible endpoints
- **Models**: type a name, or click *Fetch list* to query the provider with your key
- **End-to-end voice**: `realtime.enabled`, default `qwen3.5-omni-flash-realtime`
- **Conversation**: silence timeout, energy threshold, max utterance, barge-in, processing tone
- **DTMF**: `0` interrupts playback, `9` hangs up (both configurable)

A minimal config file looks like this:

```json
{
  "audio": { "listen": "127.0.0.1:9092" },
  "ai": {
    "base_url": "https://api.deepseek.com/v1",
    "api_key": "sk-...",
    "chat_model": "deepseek-flash",
    "asr_backend": "siliconflow",
    "tts_backend": "siliconflow"
  },
  "realtime": { "enabled": false },
  "agent": { "greeting": "Hello, how can I help you?" }
}
```

## Operations

```bash
sudo bash install.sh                 # idempotent install (~40 s on a clean box)
sudo bash upgrade.sh <new-binary>    # upgrade with automatic rollback on failure
sudo bash upgrade.sh --rollback       # roll back to the previous binary
sudo bash uninstall.sh [--purge]      # uninstall (--purge also removes config/data)
systemctl status sipvoice
journalctl -u sipvoice -f
```

## Build from source

Go 1.24+ is required. The code is **pure Go**: no cgo, no assembly, no third-party modules.

```bash
git clone https://github.com/USER/sipvoice.git && cd sipvoice
make build            # current platform
make release          # linux/amd64 + linux/arm64 tarballs in dist/release/
go test ./...
```

### Other architectures (ARMv7, MIPS, RISC-V…)

Because nothing is architecture-specific, you can cross-compile for any Linux target Go supports:

```bash
# 32-bit ARM (Raspberry Pi 2/Zero, routers)
GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" \
  -o voicebridge-linux-armv7 ./cmd/voicebridge

# little-endian MIPS (many routers / NAS)
GOOS=linux GOARCH=mipsle GOMIPS=softfloat CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" \
  -o voicebridge-linux-mipsle ./cmd/voicebridge

# RISC-V 64
GOOS=linux GOARCH=riscv64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" \
  -o voicebridge-linux-riscv64 ./cmd/voicebridge
```

Supported `GOARCH` values include `amd64`, `arm64`, `arm`, `386`, `mips`, `mipsle`, `mips64le`,
`ppc64le`, `riscv64`, `s390x`, `loong64`.

Caveats on exotic platforms: you still need a working **Asterisk** and **ffmpeg** for that
architecture from your distro (or built by yourself); the installer only knows `apt`. Officially
tested targets are `linux/amd64` and `linux/arm64`.

## Project layout

```
cmd/voicebridge/     entry point (server / agent / calltest / doctor / mockai modes)
internal/
  audiosock/         AudioSocket protocol and connection handling
  voice/             VAD, barge-in, turn orchestration
  omni/              end-to-end voice sessions (Qwen-Omni-Realtime)
  ws/                minimal WebSocket client (no third-party dependency)
  adapters/          cloud ASR/TTS/chat, local whisper, model listing
  webui/             embedded console and REST API
  phones/            extension inventory, renders Asterisk config
  auth/ config/ models/ media/ netutil/ sysinfo/ ...
deploy/              install / upgrade / uninstall scripts, Asterisk baseline configs
docs/                screenshots, release process
```

## Troubleshooting

| Symptom | Check |
|---|---|
| Console unreachable | `systemctl status sipvoice`; self-signed cert → accept it once in the browser |
| Phone registers but AI is silent | `journalctl -u sipvoice -f`; provider key and model name |
| Extensions missing after reboot | handled by a systemd drop-in that reloads PJSIP; check `asterisk -rx "pjsip show endpoints"` |
| No audio at all | `ss -lntp` shows `127.0.0.1:9092`; Asterisk must reach the AudioSocket |

## Roadmap

- [x] SIP signalling, AudioSocket bridge, VAD/barge-in
- [x] Pipeline and end-to-end voice, web console, one-command deploy
- [x] Provider presets, live model list, TTS/ASR online tests, alert push
- [ ] Fax: TIFF → PDF and download from the console
- [ ] `.deb` / `.rpm` packages
- [ ] Docker image
- [ ] More end-to-end voice providers

## Contributing

Issues and pull requests are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md). Since this code was
AI-generated, a careful human review of any change is especially valuable.

## License

MIT — see [LICENSE](LICENSE).

Runtime components (Asterisk, ffmpeg, whisper.cpp, cloud APIs) are **not** bundled; see
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) for their licenses and how they are used.
