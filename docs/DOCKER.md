# 用 Docker 运行 SIPVoice

镜像里同时跑 **Asterisk 22** 和 **voicebridge**，对外只需要两个容器级配置：网络和卷。

## 最快的一条命令

```bash
docker run -d --name sipvoice --restart unless-stopped --network host \
  -v sipvoice-etc:/etc/sipvoice \
  -v sipvoice-models:/opt/sipvoice/models \
  ghcr.io/USER/sipvoice:latest
```

然后浏览器打开 `https://<主机IP>:8080`，第一次访问会让你创建管理员账号。

> **为什么用 host 网络**：SIP（5060/udp）和 RTP（10000-10100/udp）直接绑到宿主机，省掉端口映射与
> NAT 穿透。SIP 协议把地址写在报文里，容器 NAT 后经常出现"能接通但没声音"。

## 卷和环境变量

| 路径 | 说明 |
|---|---|
| `/etc/sipvoice` | 配置、API Key、管理员账号、话机台账、自签证书。**必须持久化**，否则重启丢配置 |
| `/opt/sipvoice/models` | 可选：本地 whisper 模型（在面板「模型」页下载） |

| 变量 | 默认 | 说明 |
|---|---|---|
| `SIPVOICE_WEB` | `0.0.0.0:8080` | 控制台监听地址 |
| `TZ` | 容器默认 UTC | 设成 `Asia/Shanghai` 让日志时间是本地时间 |

## 不用 host 网络时

```bash
docker run -d --name sipvoice \
  -p 5060:5060/udp \
  -p 10000-10100:10000-10100/udp \
  -p 8080:8080 \
  -v sipvoice-etc:/etc/sipvoice \
  -e TZ=Asia/Shanghai \
  sipvoice:1.3.0
```

还需要让 Asterisk 知道自己的公网/宿主地址，否则 SDP 里的地址是容器内网 IP。可以在
`/etc/asterisk/pjsip.conf` 的 `[transport-udp]` 加：

```ini
external_media_address=<宿主机IP>
external_signaling_address=<宿主机IP>
local_net=172.16.0.0/12
```

改完 `docker exec sipvoice asterisk -rx "pjsip reload"`。

## 自己构建

```bash
make linux-amd64                       # 先产出 dist/voicebridge-linux-amd64
docker build -t sipvoice:1.3.0 .       # 国内加 --build-arg APT_MIRROR=cn.archive.ubuntu.com
```

多架构（需要 buildx）：

```bash
make linux-amd64 linux-arm64
docker buildx build --platform linux/amd64,linux/arm64 -t yourrepo/sipvoice:1.3.0 --push .
```

镜像里的 `Dockerfile` 用 `TARGETARCH` 选择对应架构的二进制。

## 常用操作

```bash
docker logs -f sipvoice                       # 看日志（等同 journalctl）
docker exec -it sipvoice asterisk -rx "pjsip show endpoints"
docker exec -it sipvoice asterisk -rx "pjsip show contacts"
docker exec -it sipvoice sh                   # 进容器
docker compose down && docker compose up -d   # 改了卷/端口后重启
```

## 排错

| 现象 | 排查 |
|---|---|
| 容器起来就退出 | `docker logs sipvoice`；多半是 5060 被宿主机的 Asterisk 占用（host 网络下会冲突） |
| 能接通但没声音 | 用了 bridge 网络但没配 `external_media_address` / 没映射 RTP 端口 → 改用 `--network host` |
| 控制台打不开 | 确认 8080 没有被别的服务占用；容器内 `ss -lnt` 看是否有 `*:8080` |
| 分机注册不上 | `docker exec sipvoice asterisk -rx "pjsip show endpoints"`；若为空，等 5 秒（启动后会自动 reload） |
| 面板报"证书加载失败" | 卷权限问题：`docker exec sipvoice ls -l /etc/sipvoice/tls`，属主应为 `sipvoice` |
| 想用本地模型 | 面板「模型」页下载，模型落在 `/opt/sipvoice/models` 卷里 |

## 安全提示

- 容器内 Asterisk 以 `asterisk` 用户运行，voicebridge 以 `sipvoice` 用户运行；entrypoint 需要 root
  才能启动这两个服务，因此容器本身是 root 身份启动的。
- 不要把 5060/做公网映射；这个项目按内网设备设计。
- `/etc/sipvoice/config.json` 里有 API Key，卷要像对待密码一样对待。
