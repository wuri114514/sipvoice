#!/bin/bash
# sipvoice-backup — SIPVoice 部署备份 / 恢复辅助工具
#
# 用法:
#   sipvoice-backup config          配置级备份（几十 MB，日常用；含配置/密钥/话机/提示音/源码）
#   sipvoice-backup full            全盘备份（约 2 GB，大改动前用；不含缓存与可重装内容）
#   sipvoice-backup list            列出已有备份
#   sipvoice-backup verify <file>   校验备份完整性
#   sipvoice-backup prune [N]       每个类型只保留最近 N 个（默认 5）
#
# 产物落在 /data/backups/，文件名 <type>-<时间戳>.tar.gz，附 .sha256 与 .manifest

set -uo pipefail

BACKUP_ROOT="${BACKUP_ROOT:-/data/backups}"
STAMP="$(date +%Y%m%d-%H%M%S)"
KEEP="${KEEP:-5}"

mkdir -p "$BACKUP_ROOT"

log() { printf '%s\n' "$*"; }
die() { printf '错误: %s\n' "$*" >&2; exit 1; }

need_root() {
	[ "$(id -u)" -eq 0 ] || die "需要 root 权限"
}

# 配置级备份清单：这些丢了才是真的麻烦
CONFIG_PATHS=(
	/etc/sipvoice
	/etc/asterisk
	/etc/systemd/system/sipvoice.service.d
	/etc/systemd/system/sipvoice.service
	/etc/systemd/system/sipvoice-rec-cleanup.service
	/etc/systemd/system/sipvoice-rec-cleanup.timer
	/var/lib/sipvoice
	/opt/sipvoice/voicebridge
	/opt/sipvoice/backups
	/root/pushrepo
	/etc/cron.d
	/etc/sudoers.d
)

# 全盘备份排除项：虚拟文件系统、缓存、可重装内容、备份自身
FULL_EXCLUDES=(
	--exclude=./proc --exclude=./sys --exclude=./dev --exclude=./run
	--exclude=./tmp --exclude=./var/tmp
	--exclude=./var/cache --exclude=./var/lib/apt/lists
	--exclude=./var/lib/snapd --exclude=./snap
	--exclude=./var/log/journal
	--exclude=./data/backups
	--exclude=./data/lost+found
	--exclude=./lost+found
	--exclude=./swapfile --exclude=./swap.img
	--exclude=./root/vb-*
	--exclude=./opt/sipvoice/models
	--exclude=./root/.cache
)

write_manifest() {
	local out="$1"
	{
		echo "SIPVoice 部署备份清单"
		echo "生成时间: $(date '+%Y-%m-%d %H:%M:%S %Z')"
		echo "主机名:   $(hostname)"
		echo "系统:     $(. /etc/os-release 2>/dev/null; echo "${PRETTY_NAME:-unknown}")"
		echo "内核:     $(uname -r)"
		echo "CPU:      $(nproc) 核 / 内存 $(free -h | awk 'NR==2{print $2}')"
		echo
		echo "---- 版本 ----"
		echo "voicebridge: $(/opt/sipvoice/voicebridge -version 2>/dev/null || echo 未安装)"
		echo "asterisk:    $(asterisk -V 2>/dev/null || echo 未安装)"
		echo "推送仓库 HEAD: $(cd /root/pushrepo 2>/dev/null && git log -1 --format='%h %s' || echo 无)"
		echo
		echo "---- 磁盘 ----"
		df -h | grep -vE 'tmpfs|udev|loop'
		echo
		echo "---- 手动安装的软件包 ----"
		apt-mark showmanual 2>/dev/null
		echo
		echo "---- 单元状态 ----"
		systemctl is-enabled sipvoice asterisk 2>/dev/null
		echo
		echo "---- 目录占用 ----"
		du -sh /etc /opt /var /root /data 2>/dev/null
	} > "$out"
}

do_config() {
	need_root
	local out="$BACKUP_ROOT/config-$STAMP.tar.gz"
	log "== 配置级备份 =="
	log "  目标: $out"

	local existing=()
	for p in "${CONFIG_PATHS[@]}"; do
		[ -e "$p" ] && existing+=("$p")
	done
	[ "${#existing[@]}" -gt 0 ] || die "没有可备份的路径"

	tar czf "$out" --absolute-names --warning=no-file-changed \
		"${existing[@]}" 2>/dev/null

	finish "$out" "config"
}

do_full() {
	need_root
	local out="$BACKUP_ROOT/full-$STAMP.tar.gz"
	log "== 全盘备份 =="
	log "  目标: $out"
	log "  排除: 虚拟文件系统 / 缓存 / snapd / journal / models / 备份自身"

	# 从根打包，排除项生效；--one-file-system 不加，/data 与 /boot/efi 要一起备
	tar czf "$out" -C / "${FULL_EXCLUDES[@]}" . 2>/dev/null

	finish "$out" "full"
}

finish() {
	local out="$1" kind="$2"
	[ -s "$out" ] || die "备份文件为空，可能失败"

	local size
	size=$(du -h "$out" | cut -f1)
	sha256sum "$out" > "$out.sha256"
	write_manifest "$out.manifest"

	log
	log "  ✅ 完成"
	log "  文件:   $out"
	log "  大小:   $size"
	log "  校验:   $(cut -d' ' -f1 "$out.sha256")"
	log "  清单:   $out.manifest"
	log
	log "  恢复提示:"
	if [ "$kind" = "config" ]; then
		log "    tar xzf $out -C /"
		log "    systemctl restart sipvoice && asterisk -rx 'dialplan reload'"
	else
		log "    建议在全新系统上解包: tar xzf $out -C /"
		log "    然后重装软件包（清单里的 apt-mark showmanual），再重启服务"
	fi
}

do_list() {
	log "== 备份目录: $BACKUP_ROOT =="
	if ! ls -1 "$BACKUP_ROOT"/*.tar.gz >/dev/null 2>&1; then
		log "  （还没有备份）"
		return
	fi
	printf '%-42s %8s  %s\n' "文件" "大小" "时间"
	for f in $(ls -1t "$BACKUP_ROOT"/*.tar.gz); do
		printf '%-42s %8s  %s\n' "$(basename "$f")" "$(du -h "$f" | cut -f1)" "$(stat -c %y "$f" | cut -d. -f1)"
	done
	log
	log "总计: $(du -sh "$BACKUP_ROOT" | cut -f1)"
}

do_verify() {
	local f="${1:-}"
	[ -n "$f" ] || die "用法: sipvoice-backup verify <备份文件>"
	[ "$(id -u)" -eq 0 ] && f="$f" || true
	[ -f "$f" ] || f="$BACKUP_ROOT/$f"
	[ -f "$f" ] || die "找不到文件: $f"

	log "== 校验 $(basename "$f") =="
	if [ -f "$f.sha256" ]; then
		if sha256sum -c "$f.sha256" --status 2>/dev/null; then
			log "  ✅ sha256 匹配"
		else
			log "  ❌ sha256 不匹配，文件可能损坏"
			return 1
		fi
	else
		log "  ⚠️ 没有 .sha256 文件，跳过校验和比对"
	fi

	log "  测试解包（只读列出前 5 项）:"
	tar tzf "$f" 2>/dev/null | head -5 | sed 's/^/    /'
	log "  ✅ 归档可正常读取"
}

do_prune() {
	local n="${1:-$KEEP}"
	for kind in config full; do
		local files
		files=$(ls -1t "$BACKUP_ROOT"/$kind-*.tar.gz 2>/dev/null || true)
		[ -n "$files" ] || continue
		local count
		count=$(echo "$files" | wc -l)
		[ "$count" -gt "$n" ] || continue
		echo "$files" | tail -n +$((n + 1)) | while read -r f; do
			log "  删除旧备份: $(basename "$f")"
			rm -f "$f" "$f.sha256" "$f.manifest"
		done
	done
	log "✅ 保留策略: 每类最近 $n 个"
}

case "${1:-}" in
config) do_config ;;
full) do_full ;;
list) do_list ;;
verify) shift; do_verify "${1:-}" ;;
prune) shift; do_prune "${1:-$KEEP}" ;;
*)
	cat <<'EOF'
sipvoice-backup — SIPVoice 部署备份工具

用法:
  sipvoice-backup config          配置级备份（小，日常用）
  sipvoice-backup full            全盘备份（约 2 GB，大改动前用）
  sipvoice-backup list            列出已有备份
  sipvoice-backup verify <文件>   校验完整性
  sipvoice-backup prune [N]       每类只保留最近 N 个（默认 5）

备份目录: /data/backups
EOF
	;;
esac
