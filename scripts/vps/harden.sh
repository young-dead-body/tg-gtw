#!/usr/bin/env bash
# Базовая защита VPS под шлюз (docs/deploy/VPS.md §3). Идемпотентен.
# Запускается на машине деплоя, действует на VPS по ssh (нужен вход по ключу).
#
#   1. unattended-upgrades: автоустановка обновлений безопасности + сразу
#      ставит накопившиеся (без автоперезагрузки — о ней скрипт сообщит);
#   2. sshd: только ключи, root только по ключу, MaxAuthTries 3,
#      LoginGraceTime 30, X11Forwarding no, ClientAlive. После reload —
#      проверка НОВОГО ssh-подключения; не прошла — откат;
#   3. ufw: deny incoming, 22/tcp с ограничением частоты (limit), 80, 443 и
#      UFW_EXTRA_ALLOW из .env.vps (например "7000/tcp 7000/udp");
#      существующие правила не удаляются;
#   4. sysctl: запрет ICMP-redirect и source routing, SYN cookies.
#
# Docker публикует порты в обход ufw: защита стека — привязка портов к
# 127.0.0.1 (docker-compose.vps.yml).
#
# Usage: harden.sh [--dry-run]
set -euo pipefail
source "$(dirname "$0")/lib.sh"

DRY=0
[ "${1:-}" = "--dry-run" ] && DRY=1
UFW_EXTRA_ALLOW="${UFW_EXTRA_ALLOW:-$(env_get "$VPS_ENV" UFW_EXTRA_ALLOW)}"

log "VPS $VPS_HOST (dry-run=$DRY), доп. порты ufw: ${UFW_EXTRA_ALLOW:-нет}"

vps_ssh "DRY=$DRY UFW_EXTRA_ALLOW='$UFW_EXTRA_ALLOW' bash -s" <<'REMOTE'
set -euo pipefail
run() { if [ "$DRY" = 1 ]; then echo "  would: $*"; else echo "  + $*"; eval "$@"; fi; }
export DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a

echo "== 1. обновления безопасности"
installed() { dpkg-query -W -f='${Status}' "$1" 2>/dev/null | grep -q 'install ok installed'; }
if ! installed unattended-upgrades || ! installed ufw; then
  run "apt-get update -qq && apt-get install -y -qq unattended-upgrades ufw >/dev/null"
fi
run "printf 'APT::Periodic::Update-Package-Lists \"1\";\nAPT::Periodic::Unattended-Upgrade \"1\";\nAPT::Periodic::AutocleanInterval \"7\";\n' > /etc/apt/apt.conf.d/20auto-upgrades"
run "systemctl enable --now apt-daily.timer apt-daily-upgrade.timer >/dev/null 2>&1 || true"
echo "  ожидают обновления безопасности: $(apt list --upgradable 2>/dev/null | grep -c -- '-security' || true)"
run "unattended-upgrade >/dev/null 2>&1 || unattended-upgrade -d | tail -20"

echo "== 2. sshd"
F=/etc/ssh/sshd_config.d/01-gateway-hardening.conf
if [ ! -s /root/.ssh/authorized_keys ]; then
  echo "  ПРОПУСК: у root нет authorized_keys — отключение паролей заблокирует вход" >&2
else
  [ -f "$F" ] && cp "$F" "/root/$(basename "$F").bak" || rm -f "/root/$(basename "$F").bak"
  run "printf '%s\n' '# scripts/vps/harden.sh' 'PasswordAuthentication no' 'KbdInteractiveAuthentication no' 'PermitRootLogin prohibit-password' 'MaxAuthTries 3' 'LoginGraceTime 30' 'X11Forwarding no' 'ClientAliveInterval 300' 'ClientAliveCountMax 2' > $F"
  if [ "$DRY" = 0 ]; then
    if sshd -t; then systemctl reload ssh; echo "  sshd перечитан"
    else echo "  sshd -t не прошёл — откат" >&2; rm -f "$F"; exit 1; fi
  fi
fi

echo "== 3. ufw"
rules=$(ufw status 2>/dev/null || true)
run "ufw default deny incoming >/dev/null && ufw default allow outgoing >/dev/null"
run "ufw limit 22/tcp comment 'ssh (rate limit)' >/dev/null"
run "ufw allow 80/tcp comment 'http: ACME + redirect' >/dev/null"
run "ufw allow 443/tcp comment 'https: gateway' >/dev/null"
for p in $UFW_EXTRA_ALLOW; do run "ufw allow $p >/dev/null"; done
if ! grep -q '^Status: active' <<<"$rules"; then
  run "ufw --force enable"
fi

echo "== 4. sysctl"
run "printf '%s\n' 'net.ipv4.conf.all.send_redirects = 0' 'net.ipv4.conf.default.send_redirects = 0' 'net.ipv4.conf.all.accept_redirects = 0' 'net.ipv4.conf.default.accept_redirects = 0' 'net.ipv4.conf.all.secure_redirects = 0' 'net.ipv6.conf.all.accept_redirects = 0' 'net.ipv6.conf.default.accept_redirects = 0' 'net.ipv4.conf.all.accept_source_route = 0' 'net.ipv6.conf.all.accept_source_route = 0' 'net.ipv4.tcp_syncookies = 1' 'net.ipv4.icmp_echo_ignore_broadcasts = 1' > /etc/sysctl.d/91-gateway-hardening.conf && sysctl -q -p /etc/sysctl.d/91-gateway-hardening.conf"

echo "== итог"
ufw status verbose | sed -n '1,40p'
[ -f /var/run/reboot-required ] && echo "!! нужна перезагрузка: $(tr '\n' ' ' < /var/run/reboot-required.pkgs 2>/dev/null | cut -c1-200)" || echo "перезагрузка не требуется"
REMOTE

[ "$DRY" = 1 ] && exit 0
# Новое подключение в обход мастер-сокета: убеждаемся, что вход не сломан.
if ssh "${SSH_OPTS[@]}" -o ControlMaster=no -o ControlPath=none "$VPS_HOST" true; then
  log "новое ssh-подключение после harden: OK"
else
  log "новое ssh-подключение НЕ проходит — откат sshd через открытое соединение"
  vps_ssh 'b=/root/01-gateway-hardening.conf.bak; f=/etc/ssh/sshd_config.d/01-gateway-hardening.conf; if [ -f "$b" ]; then cp "$b" "$f"; else rm -f "$f"; fi; sshd -t && systemctl reload ssh'
  die "sshd откатен; разберитесь с доступом по ключу и повторите."
fi
