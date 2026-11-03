#!/usr/bin/env bash
# Куда системный nginx VPS отправляет трафик шлюза (веб-вход, DC-мост dc1..5,
# TURN): на стек на самом VPS или на домашний стек через frp-туннель
# (docs/deploy/HOME.md). Server-блоки не меняются — переключаются только
# upstream'ы webtgk_up, tgk_dc1..5, tgk_turn:
#
#   /etc/nginx/webtgk-targets/{vps,home}.{http,stream}.conf
#   /etc/nginx/conf.d/zz-webtgk-target.conf          -> <target>.http.conf
#   /etc/nginx/streams-enabled/zz-webtgk-target.conf -> <target>.stream.conf
#
# Переключатель создаёт scripts/vps/setup-nginx.sh (таргет vps по умолчанию).
#
# Usage: switch-ingress.sh status|vps|home
set -euo pipefail
source "$(dirname "$0")/lib.sh"

ACTION="${1:-status}"
SRC="$PROD_DIR/infra/vps/nginx/targets"

upload_targets() {
  local stage
  stage="$(mktemp -d)"
  cp "$SRC/vps.http.conf" "$SRC/vps.stream.conf" "$stage/"
  render "$SRC/home.http.conf.template"   > "$stage/home.http.conf"
  render "$SRC/home.stream.conf.template" > "$stage/home.stream.conf"
  tar -C "$stage" -czf - . | vps_ssh "mkdir -p /etc/nginx/webtgk-targets && tar -C /etc/nginx/webtgk-targets -xzf -"
  rm -rf "$stage"
}

case "$ACTION" in
  status)
    vps_ssh 'readlink /etc/nginx/conf.d/zz-webtgk-target.conf /etc/nginx/streams-enabled/zz-webtgk-target.conf 2>/dev/null \
      || echo "переключатель не установлен (scripts/vps/setup-nginx.sh)"'
    ;;

  vps|home)
    vps_ssh 'test -L /etc/nginx/conf.d/zz-webtgk-target.conf' \
      || die "переключатель не установлен: сначала scripts/vps/setup-nginx.sh (docs/deploy/VPS.md §5)."
    # Предпроверка: целевой стек должен отвечать, иначе пользователи получат 502.
    if [ "$ACTION" = vps ]; then
      target_web=127.0.0.1:16443
    else
      target_web="$(env_get "$VPS_ENV" INGRESS_HOME_WEB)"
      [ -n "$target_web" ] || die "INGRESS_HOME_WEB не задан в $VPS_ENV."
    fi
    vps_ssh "curl -sk -m 5 -o /dev/null https://$target_web/" \
      || die "таргет $ACTION не отвечает на $target_web — стек не запущен (или нет frp-туннеля для home)."
    upload_targets
    vps_ssh "T=$ACTION bash -s" <<'REMOTE'
set -euo pipefail
cd /etc/nginx
PREV_HTTP=$(readlink conf.d/zz-webtgk-target.conf)
PREV_STREAM=$(readlink streams-enabled/zz-webtgk-target.conf)
ln -sfn /etc/nginx/webtgk-targets/$T.http.conf conf.d/zz-webtgk-target.conf
ln -sfn /etc/nginx/webtgk-targets/$T.stream.conf streams-enabled/zz-webtgk-target.conf
if nginx -t 2>&1; then
  systemctl reload nginx
  echo "трафик шлюза -> $T"
else
  ln -sfn "$PREV_HTTP" conf.d/zz-webtgk-target.conf
  ln -sfn "$PREV_STREAM" streams-enabled/zz-webtgk-target.conf
  echo "nginx -t не прошёл — оставлен прежний таргет ($PREV_HTTP)" >&2; exit 1
fi
REMOTE
    ;;

  *) die "usage: switch-ingress.sh status|vps|home" ;;
esac
