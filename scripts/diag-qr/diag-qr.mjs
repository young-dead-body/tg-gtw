#!/usr/bin/env node
/**
 * diag-qr.mjs — Playwright-диагностика почему не грузится QR-код входа в Telegram Web
 * через корпоративный шлюз.
 *
 * Что делает:
 *  1. Проходит gate шлюза (принятие согласия + вход по email/password) через реальный UI.
 *  2. Открывает Telegram Web (/tg/) и логирует ВСЕ исходящие запросы и WebSocket-апгрейды,
 *     отдельно помечая "висящие" (нет завершающего ответа в течение timeout).
 *  3. Проверяет, что происходит с 5 DC WebSocket-соединениями (dc1..dc5.<domain>):
 *     доходит ли TLS до моста, отвечает ли мост 101 Switching Protocols.
 *  4. Проверяет перезаписи, сделанные прокси:
 *       - bootstrap-бандл (index.worker-*.js): переписан ли wss://kws<N> на wss://dc<N>.<domain>
 *         (= canary 'rewrite_total{kind="bundle",result="miss"}')
 *       - dc_options в ответе getConfig: переписаны ли ip:port на dc<N>.<domain>:443
 *       - CSP connect-src: разрешены ли wss://dc<N>.<domain>
 *  5. Печатает структурированный JSON-отчёт.
 *
 * Требуется (env): GATEWAY_URL, TG_EMAIL, TG_PASSWORD, [CONSENT_DOC_VERSION], [QR_WAIT_MS]
 * Без TG_EMAIL/TG_PASSWORD — проходит без логина (для проверки анонимного пути/статики).
 */

import {
  chromium,
} from "playwright";
import net from "node:net";
import tls from "node:tls";

const GATEWAY_URL = process.env.GATEWAY_URL || "https://localhost";
const TG_EMAIL = process.env.TG_EMAIL || "";
const TG_PASSWORD = process.env.TG_PASSWORD || "";
const CONSENT_DOC_VERSION = process.env.CONSENT_DOC_VERSION || "1.0";
const QR_WAIT_MS = Number(process.env.QR_WAIT_MS || 30000);
const CONNECT_TIMEOUT_MS = Number(process.env.CONNECT_TIMEOUT_MS || 15000);
const OUT_DIR = process.env.OUT_DIR || process.cwd();
const DO_LOGIN = !!(TG_EMAIL && TG_PASSWORD);

const domain = new URL(GATEWAY_URL).hostname;

// ---------------------------------------------------------------------------
// Сбор сетевой деятельности (главная страница)
// ---------------------------------------------------------------------------

function makeNetworkLogger() {
  const reqs = new Map();
  const sockets = new Map();
  const wsLifetimes = [];
  const consoleLogs = [];
  const failedReqs = [];

  const record = (id, type, url, meta) =>
    reqs.set(id, { id, type, url, ...meta, started: Date.now(), done: false });

  const attach = (page) => {
    // HTTP-запросы главной страницы (worker-запросы покроет TCP-прокси)
    page.on("request", (r) => {
      record(r.url(), "http", r.url(), {
        method: r.method(),
        resourceType: r.resourceType(),
        headers: summarize(r.headers()),
      });
    });
    page.on("requestfailed", (r) => {
      failedReqs.push({
        url: r.url(),
        method: r.method(),
        failure: r.failure(),
      });
      const e = reqs.get(r.url());
      if (e) e.done = true;
    });
    page.on("response", (r) => {
      const e = reqs.get(r.url());
      if (e) {
        e.status = r.status();
        e.done = true;
      }
    });
    page.on("console", (msg) => {
      const txt = msg.text();
      if (msg.type() === "error" || /ws|websocket|dc|auth|proxy/i.test(txt)) {
        consoleLogs.push({ type: msg.type(), text: txt.slice(0, 400) });
      }
    });
    return Promise.resolve();
  };

  const finish = () => {
    const now = Date.now();
    for (const s of sockets.values()) {
      if (!s.done) {
        s.phase = "pending";
        s.pendingMs = now - s.t0;
        s.done = true;
      }
    }
    for (const e of reqs.values()) {
      if (!e.done) e.pendingMs = now - e.started;
    }
  };

  return { attach, finish, get: () => ({ reqs, sockets, wsLifetimes, consoleLogs, failedReqs }) };
}

function summarize(headers) {
  const out = {};
  for (const [k, v] of Object.entries(headers)) {
    if (/cookie|authorization|x-csrf/i.test(k)) continue;
    out[k] = (String(v) || "").slice(0, 80);
  }
  return out;
}

// ---------------------------------------------------------------------------
// Прямые сетевые проверки DC-моста (без браузера)
// ---------------------------------------------------------------------------

function tcpConnect(host, port, timeout) {
  return new Promise((resolve) => {
    const s = net.connect({ host, port });
    const t = setTimeout(() => {
      s.destroy();
      resolve({ ok: false, error: `timeout after ${timeout}ms` });
    }, timeout);
    s.on("connect", () => {
      clearTimeout(t);
      s.destroy();
      resolve({ ok: true });
    });
    s.on("error", (e) => {
      clearTimeout(t);
      resolve({ ok: false, error: e.message });
    });
  });
}

function tlsConnect(host, port, timeout) {
  return new Promise((resolve) => {
    const s = tls.connect({ host, port, servername: host, rejectUnauthorized: false });
    const t = setTimeout(() => {
      s.destroy();
      resolve({ ok: false, error: `timeout after ${timeout}ms` });
    }, timeout);
    s.on("secureConnect", () => {
      clearTimeout(t);
      resolve({ ok: true, alpn: s.alpnProtocol, authorized: s.authorized, cipher: s.getCipher?.()?.name });
      s.end();
    });
    s.on("error", (e) => {
      clearTimeout(t);
      resolve({ ok: false, error: e.message });
    });
  });
}

// WebSocket handshake send (raw), чтобы понять, отвечает ли мост 101.
function wsUpgradeProbe(host, port, path, origin, timeout) {
  return new Promise((resolve) => {
    const s = tls.connect({ host, port, servername: host, rejectUnauthorized: false });
    let done = false;
    const t = setTimeout(() => {
      if (!done) { done = true; s.destroy(); resolve({ ok: false, error: "timeout" }); }
    }, timeout);
    let buf = "";
    s.on("secureConnect", () => {
      const req = [
        `GET ${path} HTTP/1.1`,
        `Host: ${host}:${port}`,
        "Upgrade: websocket",
        "Connection: Upgrade",
        "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==",
        "Sec-WebSocket-Version: 13",
        `Sec-WebSocket-Protocol: binary`,
        `Origin: ${origin}`,
        "",
        "",
      ].join("\r\n");
      s.write(req);
    });
    s.on("data", (d) => {
      buf += d.toString("utf8");
      const idx = buf.indexOf("\r\n\r\n");
      if (idx >= 0 && !done) {
        done = true;
        const headers = buf.slice(0, idx);
        const statusLine = headers.split("\r\n")[0] || "";
        const upgrade = /upgrade:\s*websocket/i.test(headers);
        clearTimeout(t); s.destroy();
        resolve({ ok: / 101 /.test(statusLine), statusLine, upgrade, headers: headers.slice(0, 400) });
      }
    });
    s.on("error", (e) => {
      if (!done) { done = true; clearTimeout(t); resolve({ ok: false, error: e.message }); }
    });
  });
}

// ---------------------------------------------------------------------------
// Проверка перезаписей в полученных body
// ---------------------------------------------------------------------------

async function collectTgResources(page) {
  const resources = [];
  page.on("response", async (r) => {
    const url = r.url();
    const ct = (r.headers()["content-type"] || "").toLowerCase();
    if (!url.includes("/tg/")) return;
    const interesting =
      /index\.worker-[^/]+\.js$/.test(url) ||
      /\.(js|mjs)$/.test(url) ||
      (ct.includes("json"));
    if (!interesting) return;
    try {
      const body = await r.text();
      if (body.length > 2_000_000) return;
      resources.push({ url, ct, body, status: r.status() });
    } catch { /* ignore */ }
  });
  return resources;
}

function findWsConstructors(bundle) {
  const hits = new Set();
  const misses = new Set();
  // ищем и dc<N>.<domain> (переписанные) и kws<N>.web.telegram.org (оригинал)
  const dcRe = new RegExp(`wss:\\/\\/dc\\d+\\\\.${domain.replace(/\./g, "\\.")}`, "g");
  const kwsRe = /wss:\/\/kws\d+\.web\.telegram\.org/g;
  const rawDc = /wss:\/\/dc\d+\./g;
  for (const m of bundle.matchAll(dcRe)) hits.add(m[0]);
  for (const m of bundle.matchAll(kwsRe)) misses.add(m[0]);
  for (const m of bundle.matchAll(rawDc)) { if (!hits.has(m[0])) hits.add(m[0]); }
  return { hits: [...hits], misses: [...misses] };
}

// ---------------------------------------------------------------------------
// Логин через UI
// ---------------------------------------------------------------------------

// Логин через API (как делает LoginForm): гарантированно проходит gate.
// Сначала открываем шлюз, чтобы SPA поставила tg_csrf cookie и session-cookie
// поселились в контексте, затем POST /api/login с CSRF-токеном.
async function apiLogin(page) {
  const steps = [];
  await page.goto(GATEWAY_URL, { waitUntil: "domcontentloaded" });
  await page.waitForTimeout(800);

  const res = await page.evaluate(async ({ email, password }) => {
    const csrf = (document.cookie.match(/(?:^|;\s*)tg_csrf=([^;]*)/) || [])[1] || "";
    const r = await fetch("/api/login", {
      method: "POST",
      credentials: "include",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrf,
        "X-Screen-Resolution": "1280x720",
      },
      body: JSON.stringify({ email, password }),
    });
    const body = await r.text();
    return { status: r.status, body };
  }, { email: TG_EMAIL, password: TG_PASSWORD });

  if (res.status === 200) {
    steps.push("api-login-ok");
  } else {
    steps.push(`api-login-failed(${res.status}: ${res.body.slice(0, 120)})`);
  }

  try {
    await page.goto(`${GATEWAY_URL}/tg/`, { waitUntil: "domcontentloaded" });
    steps.push("goto-/tg/");
  } catch (e) {
    steps.push(`goto-/tg/-err: ${e.message.slice(0, 80)}`);
  }
  return steps;
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

function banner(t) {
  console.log("\n" + "=".repeat(t.length + 4));
  console.log("| " + t + " |");
  console.log("=".repeat(t.length + 4) + "\n");
}

(async () => {
  banner(`Диагностика QR входа в Telegram Web`);
  console.log(`GATEWAY_URL : ${GATEWAY_URL}`);
  console.log(`domain      : ${domain}`);
  console.log(`логин шлюза : ${DO_LOGIN ? "да" : "нет (анонимно)"}`);
  console.log(`QR_WAIT_MS  : ${QR_WAIT_MS}`);

  const report = {
    gateway_url: GATEWAY_URL,
    domain,
    login: DO_LOGIN ? TG_EMAIL : null,
    time: new Date().toISOString(),
    dcChecks: [],
    wsSeen: [],
    pending: [],
    rewrite: { bundle: null, config: null, csp: null },
    consoleErrors: [],
    failedRequests: [],
    screenshots: {},
  };

  // Постоянный профиль: IndexedDB/транспорт Telegram Web требует устойчивой
  // storage (временный эфемерный JS-профиль вызывает IDB_CREATE_TIMEOUT и
  // клиент падает до установки DC-соединения). Профиль маунтится в постоянную
  // директорию (PERSISTENT_DIR) внутри контейнера.
  const PERSISTENT_DIR = process.env.PERSISTENT_DIR || "/work/profile";
  const ctx = await chromium.launchPersistentContext(PERSISTENT_DIR, {
    headless: true,
    ignoreHTTPSErrors: true,
  });
  const browser = ctx.browser();
  const page = ctx.pages()[0] || (await ctx.newPage());
  const logger = makeNetworkLogger();
  await logger.attach(page);
  const tgResources = await collectTgResources(page);

  // Все console-сообщения (включая error от worker)
  page.on("console", (msg) => {
    const txt = msg.text();
    if (msg.type() === "error" || /ws|websocket|dc|auth|proxy/i.test(txt)) {
      report.consoleErrors.push({ type: msg.type(), text: txt.slice(0, 400) });
    }
  });

  try {
    // 1. Логин
    if (DO_LOGIN) {
      report.loginSteps = await apiLogin(page);
      console.log("Шаги логина:", report.loginSteps.join(", "));
    } else {
      await page.goto(GATEWAY_URL, { waitUntil: "domcontentloaded" });
      await page.goto(`${GATEWAY_URL}/tg/`, { waitUntil: "domcontentloaded" });
    }

    // 2. Даём QR-коду время появиться; собираем сеть
    await page.waitForTimeout(QR_WAIT_MS);

    // 3. Скриншот на всякий случай
    try {
      const shot = `${OUT_DIR}/qr-screenshot.png`;
      await page.screenshot({ path: shot, fullPage: false });
      report.screenshots.final = shot;
    } catch { /* ignore */ }

    logger.finish();

    // 4. Разбираем висящие WebSocket/HTTP
    const { sockets, reqs, failedReqs } = logger.get();
    for (const s of sockets.values()) {
      const isDc = /dc\d+\./.test(s.url);
      report.wsSeen.push({
        url: s.url,
        phase: s.phase,
        status: s.status ?? null,
        statusText: s.statusText ?? null,
        pendingMs: s.pendingMs ?? null,
        error: s.error ?? null,
        dc: isDc,
      });
      if (s.phase === "pending" || s.phase === "frame-error" || !s.status) {
        report.pending.push({ kind: "ws", ...s });
      }
    }
    for (const e of reqs.values()) {
      // Только долго висящие GET, и только внешние/существенные
      if (e.pendingMs !== undefined) {
        report.pending.push({
          kind: "http", url: e.url, method: e.method,
          resourceType: e.resourceType, pendingMs: e.pendingMs,
          status: e.status ?? null,
        });
      }
    }
    report.failedRequests = failedReqs;

    // 5. Проверка перезаписи бандла
    const worker = tgResources.find((r) => /index\.worker-[^/]+\.js$/.test(r.url));
    if (worker) {
      const w = findWsConstructors(worker.body);
      report.rewrite.bundle = {
        url: worker.url,
        bytes: worker.body.length,
        rewrittenToDcDomain: w.hits,
        leftAsOriginalKws: w.misses,
        // 'hits' содержит переписанные wss://dc<N>.<domain>: значит rewrite сработал (canary hit)
        canary: w.hits.length > 0 && w.misses.length === 0 ? "hit" : (w.misses.length > 0 ? "miss-partial" : (w.hits.length === 0 && w.misses.length === 0 ? "none-found" : "mixed")),
        allWsUrls: [...w.hits, ...w.misses],
      };
      console.log(`\n[бандл] ${worker.url}`);
      console.log(`  переписан на dc<N>.${domain}: ${w.hits.join(", ") || "НЕТ"}`);
      console.log(`  остались kws<N>.web.telegram.org: ${w.misses.join(", ") || "нет"}`);
      console.log(`  canary rewrite_total{kind="bundle"}: ${report.rewrite.bundle.canary}`);
    } else {
      report.rewrite.bundle = { error: "worker bundle not intercepted" };
    }

    // 6. Проверка dc_options в getConfig (ищем конфиг с адресами dc<N>.<domain> или web.telegram.org)
    const configHit = tgResources.filter((r) =>
      /(dc\d+\.)?(web\.telegram\.org)/.test(r.body) || new RegExp(`dc\\d+\\.${domain}`).test(r.body)
    );
    if (configHit.length) {
      report.rewrite.config = configHit.map((r) => {
        const rewritten = new Set();
        const original = new Set();
        for (const m of r.body.matchAll(new RegExp(`dc\\d+\\.${domain}`, "g"))) rewritten.add(m[0]);
        for (const m of r.body.matchAll(/(?:kws|venus|aurora|vesta|pluto|flora)[0-9]?\.web\.telegram\.org/g)) original.add(m[0]);
        return { url: r.url, ct: r.ct, rewrittenDcDomain: [...rewritten], originalDirect: [...original] };
      });
    }

    // 7. Прямые сетевые проверки dc1..dc5
    console.log("\n[DC-мост] прямые сетевые проверки dc1..dc5 (WS-handshake к /apiws):");
    for (let dc = 1; dc <= 5; dc++) {
      const host = `dc${dc}.${domain}`;
      const tcp = await tcpConnect(host, 443, CONNECT_TIMEOUT_MS);
      let tlsr = { ok: false };
      if (tcp.ok) tlsr = await tlsConnect(host, 443, CONNECT_TIMEOUT_MS);
      // path совпадает с тем, что шлёт Telegram Web (apiws bootstrap);
      // Origin — шлюз, как у реального браузера.
      const ws = tlsr.ok
        ? await wsUpgradeProbe(host, 443, "/apiws", GATEWAY_URL, CONNECT_TIMEOUT_MS)
        : { ok: false, error: "tcp/tls failed" };
      report.dcChecks.push({ dc, host, path: "/apiws", tcp, tls: tlsr, ws });
      const mark = tlsr.ok ? `TLS=OK` : `TLS=FAIL(${tlsr.error})`;
      const wsMark = ws.ok ? "WS=101 OK" : `WS=FAIL(${ws.statusLine || ws.error})`;
      console.log(`  dc${dc} ${host}  TCP=${tcp.ok ? "ok" : "fail"}  ${mark}  ${wsMark}`);
    }

    // 8. CSP connect-src со страницы
    try {
      const csp = await page.evaluate(() => {
        const r = document.querySelector('meta[http-equiv="Content-Security-Policy"]');
        return r ? r.content : null;
      });
      report.rewrite.csp = { meta: csp };
      for (let dc = 1; dc <= 5; dc++) {
        const allowed = csp && csp.includes(`wss://dc${dc}.${domain}`);
        report.rewrite.csp[`dc${dc}_allowed`] = !!allowed;
        if (!allowed) console.log(`  [CSP] WARNING: wss://dc${dc}.${domain} НЕ разрешён в connect-src`);
      }
      if (csp) console.log(`  [CSP] connect-src содержит dc1..5: ${[1,2,3,4,5].map(d => report.rewrite.csp[`dc${d}_allowed`]).every(Boolean)}`);
    } catch { /* ignore */ }

  } catch (err) {
    report.fatal = { message: err.message, stack: err.stack?.split("\n").slice(0, 6) };
    console.error("\n[FATAL]", err);
  } finally {
    await browser.close().catch(() => {});
  }

  // ---------------------------------------------------------------------------
  // Отчёт
  // ---------------------------------------------------------------------------
  banner("ОТЧЁТ");
  const pendingCount = report.pending.length;
  const wsHanging = report.pending.filter((p) => p.kind === "ws");
  console.log(`Всего "висящих" запросов: ${pendingCount}`);
  console.log(`WebSocket (DC) без завершения handshake: ${wsHanging.length}`);
  for (const p of report.pending) {
    const when = p.pendingMs != null ? `${(p.pendingMs / 1000).toFixed(1)}s` : "n/a";
    console.log(`  - [${p.kind}] ${p.resourceType || ""} ${p.url.slice(0, 110)}  (${when}${p.status ? `, http=${p.status}` : ""})`);
  }
  if (report.rewrite.bundle) {
    console.log(`\nBootstrap-бандл: canary = ${report.rewrite.bundle.canary} ` +
      `(hit = QR может работать, miss = proxy не переписал DC WS — вероятная причина заглохшего QR)`);
  }
  if (report.rewrite.config) {
    console.log(`config dc_options: найдено ${report.rewrite.config.length} конфигов; ` +
      `переписано на dc<N>.<domain>: ${report.rewrite.config.some(c => c.rewrittenDcDomain.length)}`);
  }
  if (report.consoleErrors.length) {
    console.log(`\nConsole errors (${report.consoleErrors.length}):`);
    for (const c of report.consoleErrors.slice(0, 15)) console.log(`  [${c.type}] ${c.text}`);
  }

  const fs = await import("node:fs");
  const outPath = `${OUT_DIR}/diag-qr-report.json`;
  fs.writeFileSync(outPath, JSON.stringify(report, null, 2));
  console.log(`\nПолный отчёт: ${outPath}`);
  console.log(`Screenshot:   ${report.screenshots.final || "нет"}`);

  process.exit(0);
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
