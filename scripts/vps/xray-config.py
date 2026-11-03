#!/usr/bin/env python3
"""Конфиг xray-клиента для VPS-стека из ссылок vless:// (docs/deploy/VPS.md §2).

Читает из env-файла XRAY_LINK_1, XRAY_LINK_2, ... (в порядке номеров) и
XRAY_SOCKS_UDP_IP, печатает JSON-конфиг xray в stdout:

  - inbound: SOCKS5 :10808 (UDP включён, адрес UDP-релея = XRAY_SOCKS_UDP_IP);
  - outbound на каждую ссылку (proxy-1, proxy-2, ...): VLESS поверх
    tcp/xhttp/ws/grpc, security reality/tls/none;
  - одна ссылка — весь трафик в неё; несколько — observatory проверяет узлы
    раз в минуту, балансировщик выбирает живой с наименьшей задержкой,
    при недоступности всех — proxy-1;
  - адреса частных сетей блокируются (SOCKS не ведёт внутрь VPS).

Usage: xray-config.py <env-file>
"""
import json
import re
import sys
from urllib.parse import parse_qs, unquote, urlsplit


def read_env(path):
    env = {}
    with open(path, encoding="utf-8") as f:
        for line in f:
            m = re.match(r"^([A-Z][A-Z0-9_]*)=(.*)$", line.rstrip("\n"))
            if m:
                val = m.group(2).strip()
                if len(val) >= 2 and val[0] == val[-1] and val[0] in "'\"":
                    val = val[1:-1]
                env[m.group(1)] = val
    return env


def outbound(link, tag):
    u = urlsplit(link)
    if u.scheme != "vless":
        raise ValueError(f"{tag}: поддерживаются только ссылки vless://, получено {u.scheme}://")
    if not u.username or not u.hostname or not u.port:
        raise ValueError(f"{tag}: в ссылке нет uuid@host:port")
    q = {k: v[0] for k, v in parse_qs(u.query, keep_blank_values=True).items()}
    network = q.get("type", "tcp")
    security = q.get("security", "none")

    user = {"id": unquote(u.username), "encryption": q.get("encryption", "none")}
    if q.get("flow"):
        user["flow"] = q["flow"]

    stream = {"network": network, "security": security}
    if security == "reality":
        stream["realitySettings"] = {
            "serverName": q.get("sni", ""),
            "fingerprint": q.get("fp", "chrome"),
            "publicKey": q.get("pbk", ""),
            "shortId": q.get("sid", ""),
            "spiderX": q.get("spx", "/"),
        }
    elif security == "tls":
        tls = {"serverName": q.get("sni", u.hostname), "fingerprint": q.get("fp", "chrome")}
        if q.get("alpn"):
            tls["alpn"] = q["alpn"].split(",")
        stream["tlsSettings"] = tls

    if network == "xhttp":
        xhttp = {"path": q.get("path", "/"), "mode": q.get("mode", "auto")}
        if q.get("host"):
            xhttp["host"] = q["host"]
        if q.get("extra"):
            xhttp["extra"] = json.loads(q["extra"])
        elif q.get("x_padding_bytes"):
            xhttp["extra"] = {"xPaddingBytes": q["x_padding_bytes"]}
        stream["xhttpSettings"] = xhttp
    elif network == "ws":
        stream["wsSettings"] = {"path": q.get("path", "/"), "host": q.get("host", "")}
    elif network == "grpc":
        stream["grpcSettings"] = {"serviceName": q.get("serviceName", "")}
    elif network != "tcp":
        raise ValueError(f"{tag}: транспорт type={network} не поддерживается")

    return {
        "tag": tag,
        "protocol": "vless",
        "settings": {"vnext": [{"address": u.hostname, "port": u.port, "users": [user]}]},
        "streamSettings": stream,
    }


def main():
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    env = read_env(sys.argv[1])
    keys = sorted((k for k in env if re.fullmatch(r"XRAY_LINK_\d+", k) and env[k]),
                  key=lambda k: int(k.rsplit("_", 1)[1]))
    if not keys:
        sys.exit("xray-config: нет ни одной XRAY_LINK_N в env-файле")
    outbounds = [outbound(env[k], f"proxy-{i}") for i, k in enumerate(keys, 1)]

    rules = [{"type": "field", "ip": ["geoip:private"], "outboundTag": "block"}]
    config = {
        "log": {"loglevel": "warning"},
        "inbounds": [{
            "tag": "socks",
            "listen": "0.0.0.0",
            "port": 10808,
            "protocol": "socks",
            "settings": {"auth": "noauth", "udp": True,
                         "ip": env.get("XRAY_SOCKS_UDP_IP", "127.0.0.1")},
            "sniffing": {"enabled": True, "destOverride": ["http", "tls", "quic"], "routeOnly": True},
        }],
        "outbounds": outbounds + [{"tag": "block", "protocol": "blackhole"}],
        "routing": {"domainStrategy": "AsIs", "rules": rules},
    }
    if len(outbounds) > 1:
        config["observatory"] = {
            "subjectSelector": ["proxy-"],
            "probeUrl": "https://www.gstatic.com/generate_204",
            "probeInterval": "1m",
            "enableConcurrency": True,
        }
        config["routing"]["balancers"] = [{
            "tag": "egress",
            "selector": ["proxy-"],
            "strategy": {"type": "leastPing"},
            "fallbackTag": "proxy-1",
        }]
        rules.append({"type": "field", "network": "tcp,udp", "balancerTag": "egress"})
    json.dump(config, sys.stdout, indent=2, ensure_ascii=False)
    print()


if __name__ == "__main__":
    try:
        main()
    except (ValueError, json.JSONDecodeError) as e:
        sys.exit(f"xray-config: {e}")
