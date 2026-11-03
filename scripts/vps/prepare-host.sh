#!/usr/bin/env bash
# Однократная подготовка VPS (1 vCPU / 1 ГБ) под прод-стек: docs/deploy/VPS.md §2.
# Запускается на машине деплоя, выполняет действия на VPS по ssh. Идемпотентен.
#
#   - swap-файл 1 ГБ + vm.swappiness=10 (без swap OOM-killer снимает postgres);
#   - журнал systemd: не больше 50 МБ на диске и 16 МБ в ОЗУ;
#   - отключает ненужные на VPS службы (fwupd, ModemManager, udisks2, upower,
#     multipathd, networkd-dispatcher) — ~70 МБ ОЗУ;
#   - отключает motd-скрипты, которые при каждом ssh-входе запускают
#     check-new-release (~100 МБ на время входа);
#   - ставит Docker + compose-плагин, если их нет (Ubuntu/Debian, apt);
#   - создаёт каталог стека $VPS_DIR (chmod 700).
#
# Usage: prepare-host.sh [--dry-run]
set -euo pipefail
source "$(dirname "$0")/lib.sh"

DRY=0
[ "${1:-}" = "--dry-run" ] && DRY=1

log "VPS $VPS_HOST, каталог стека $VPS_DIR (dry-run=$DRY)"

vps_ssh "DRY=$DRY VPS_DIR='$VPS_DIR' bash -s" <<'REMOTE'
set -euo pipefail
run() { if [ "$DRY" = 1 ]; then echo "  would: $*"; else echo "  + $*"; eval "$@"; fi; }

echo "== swap"
if swapon --show --noheadings | grep -q .; then
  echo "  swap уже есть: $(swapon --show --noheadings | awk '{print $1, $3}')"
else
  run "fallocate -l 1G /swapfile && chmod 600 /swapfile && mkswap /swapfile >/dev/null && swapon /swapfile"
  grep -q '^/swapfile ' /etc/fstab || run "echo '/swapfile none swap sw 0 0' >> /etc/fstab"
fi
run "echo 'vm.swappiness=10' > /etc/sysctl.d/90-telegram-gateway.conf && sysctl -q -p /etc/sysctl.d/90-telegram-gateway.conf"

echo "== journald"
run "mkdir -p /etc/systemd/journald.conf.d && printf '[Journal]\nSystemMaxUse=50M\nRuntimeMaxUse=16M\n' > /etc/systemd/journald.conf.d/size.conf"
run "systemctl restart systemd-journald && journalctl --vacuum-size=50M >/dev/null"

echo "== лишние службы"
for unit in fwupd.service fwupd-refresh.timer ModemManager.service udisks2.service \
            upower.service multipathd.service multipathd.socket networkd-dispatcher.service; do
  if systemctl list-unit-files "$unit" --no-legend 2>/dev/null | grep -q .; then
    if [ "$(systemctl is-enabled "$unit" 2>/dev/null || true)" = masked ]; then
      echo "  $unit: уже masked"
    else
      run "systemctl disable --now $unit >/dev/null 2>&1 || true; systemctl mask $unit >/dev/null 2>&1"
    fi
  fi
done

echo "== motd (check-new-release при ssh-входе)"
for f in 50-motd-news 85-fwupd 90-updates-available 91-release-upgrade 91-contract-ua-esm-status 95-hwe-eol; do
  if [ -x "/etc/update-motd.d/$f" ]; then run "chmod -x /etc/update-motd.d/$f"; fi
done

echo "== Docker"
if docker compose version >/dev/null 2>&1; then
  echo "  есть: $(docker --version), compose $(docker compose version --short)"
else
  run "apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq docker.io docker-compose-v2 curl jq && systemctl enable --now docker"
fi

echo "== каталог стека"
run "mkdir -p '$VPS_DIR' && chmod 700 '$VPS_DIR'"

echo "== итог"
free -m
swapon --show
REMOTE
