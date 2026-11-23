# SIPVoice 一体化镜像：Asterisk + voicebridge 在同一容器内
#
# 构建（在仓库根目录）：
#   docker build -t sipvoice:1.3.0 .
#   docker buildx build --platform linux/arm64 -t sipvoice:1.3.0-arm64 .
#
# 运行（推荐 host 网络，省掉 SIP/RTP 的 NAT 麻烦）：
#   docker run -d --name sipvoice --restart unless-stopped --network host \
#     -v sipvoice-etc:/etc/sipvoice -v sipvoice-models:/opt/sipvoice/models \
#     sipvoice:1.3.0
FROM ubuntu:26.04

# 构建时可用 --build-arg APT_MIRROR=cn.archive.ubuntu.com 加速（国内网络）
ARG APT_MIRROR="archive.ubuntu.com"

# buildx 会自动注入 TARGETARCH；普通 docker build 时默认 amd64
ARG TARGETARCH=amd64
ENV DEBIAN_FRONTEND=noninteractive

# Asterisk 22 + 语音转码 + 运行所需的最小集合
# 注意：必须显式装 asterisk-config，否则 /etc/asterisk/asterisk.conf 不存在
RUN if [ -f /etc/apt/sources.list.d/ubuntu.sources ]; then \
        sed -i "s|archive.ubuntu.com|${APT_MIRROR}|g; s|security.ubuntu.com|${APT_MIRROR}|g" /etc/apt/sources.list.d/ubuntu.sources; \
    elif [ -f /etc/apt/sources.list ]; then \
        sed -i "s|archive.ubuntu.com|${APT_MIRROR}|g; s|security.ubuntu.com|${APT_MIRROR}|g" /etc/apt/sources.list; \
    fi \
 && apt-get update \
 && apt-get install -y --no-install-recommends \
        asterisk asterisk-config ffmpeg curl ca-certificates sudo \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --no-create-home --shell /usr/sbin/nologin sipvoice

COPY dist/voicebridge-linux-${TARGETARCH} /opt/sipvoice/voicebridge
COPY deploy/asterisk/pjsip.conf deploy/asterisk/extensions.conf deploy/asterisk/rtp.conf /etc/asterisk/
COPY deploy/config.example.json /usr/share/sipvoice/config.example.json
COPY docker/entrypoint.sh /usr/local/bin/sipvoice-entrypoint

RUN chmod 0755 /opt/sipvoice/voicebridge /usr/local/bin/sipvoice-entrypoint \
 && mkdir -p /opt/sipvoice/models /etc/sipvoice/tls /etc/sipvoice/backups \
 && chown -R sipvoice:sipvoice /opt/sipvoice /etc/sipvoice \
 && printf 'sipvoice ALL=(root) NOPASSWD: /usr/sbin/asterisk\n' \
      > /etc/sudoers.d/sipvoice-asterisk \
 && chmod 440 /etc/sudoers.d/sipvoice-asterisk

# 5060 SIP，8080 控制台，10000-10100 RTP
EXPOSE 5060/udp
EXPOSE 8080/tcp
EXPOSE 10000-10100/udp

VOLUME ["/etc/sipvoice", "/opt/sipvoice/models"]

HEALTHCHECK --interval=30s --timeout=3s --start-period=20s --retries=3 \
  CMD curl -sk -o /dev/null -w '%{http_code}' https://127.0.0.1:8080/api/status \
      | grep -qE '200|401' || exit 1

ENTRYPOINT ["/usr/local/bin/sipvoice-entrypoint"]
