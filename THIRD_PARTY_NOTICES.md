# Third-Party Notices

This repository contains only original code (MIT licensed). It has **no third-party Go modules** —
`go.mod` declares no dependencies, and there is no vendored source code.

However, SIPVoice uses other software **at runtime**. The table below explains how, and what that
means for licensing.

| Component | Used for | License | How we use it | Do we distribute it? |
|---|---|---|---|---|
| **Asterisk 22** | SIP signalling, RTP, G.711, T.38 fax | GPL v2 | Separate process; we talk to it over **AudioSocket (TCP)** and the Asterisk CLI. No linking, no code copied. | **No** — installed from the distro with `apt` by `install.sh` |
| **ffmpeg** | Transcode uploaded audio for ASR tests | LGPL-2.1+ (Ubuntu build enables GPL components) | Invoked as a child process (`ffmpeg` CLI) | **No** — distro package |
| **whisper.cpp** | Optional local speech recognition | MIT | Invoked as a child process (`whisper-cli`) | **No** — installed separately by the user |
| **edge-tts** | Optional TTS backend | GPL-3.0 | Invoked as a child process | **No** — optional, installed by the user |
| **Go standard library / runtime** | The binary itself | BSD-3-Clause | Statically compiled into `voicebridge` | **Yes** — therefore the license text below is shipped with every release |
| **Qwen-Omni / DashScope** | End-to-end voice API | Commercial API terms | HTTPS/WSS calls with the user's own key | No (network service) |
| **DeepSeek / SiliconFlow / OpenAI / Groq / …** | Chat, ASR, TTS APIs | Respective API terms | HTTPS calls with the user's own key | No (network service) |
| **ServerChan (Server酱)** | Push notifications to WeChat | Service terms | HTTPS call with the user's own SendKey | No (network service) |

## Why GPL components do not affect this project's MIT license

Asterisk and ffmpeg are **not linked** into `voicebridge`. They run as independent processes and
communicate over a TCP protocol (AudioSocket) or a command line. This is the classic
"separate programs communicating over a socket" case: our code is not a derivative work of theirs.

We also do **not redistribute** their binaries: `install.sh` calls `apt-get install asterisk ffmpeg`,
which means the user receives those packages from their distribution vendor, under that vendor's
terms. If you fork this project and decide to **bundle** Asterisk or ffmpeg inside your own
installer or image, the GPL applies to that bundle and you must comply with it (source offer,
license texts, etc.).

## Go runtime license (shipped)

```
Copyright (c) 2009 The Go Authors. All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google Inc. nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

## Trademarks

Asterisk is a trademark of Sangoma Technologies. All other product names are trademarks or
registered trademarks of their respective owners. This project is not affiliated with, endorsed by,
or sponsored by any of them.
