import { useCallback, useEffect, useState } from "react";
import { api } from "../lib/api";
import type { User } from "../lib/types";
import { useFeatures } from "../lib/features";

interface Session { id: string; expires_at: string; current: boolean }
export function Account({ user }: { user: User }) {
  const [sessions, setSessions] = useState<Session[]>([]);
  const [error, setError] = useState("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState("Проверяем подключение…");
  const features = useFeatures();
  const load = useCallback(async () => {
    try { setSessions((await api.get<{ sessions: Session[] }>("/me/sessions")).sessions); setError(""); }
    catch { setError("Не удалось загрузить сессии."); }
  }, []);
  useEffect(() => { void load(); }, [load]);
  useEffect(() => {
    const controller = new AbortController();
    void fetch("/tg/_gw/status", { credentials: "same-origin", signal: controller.signal })
      .then(async (r) => {
        if (r.status === 401) return "Сессия истекла. Войдите заново.";
        if (r.status === 403) return "Доступ ограничен. Проверьте согласие и срок действия аккаунта.";
        if (!r.ok) return "Подключение к Telegram временно недоступно.";
        const body = await r.json() as { available: boolean };
        return body.available ? "Шлюз готов к подключению." : "Telegram временно недоступен. Повторите попытку позже.";
      }).then(setStatus).catch(() => { if (!controller.signal.aborted) setStatus("Не удалось связаться со шлюзом."); });
    return () => controller.abort();
  }, []);
  async function action(fn: () => Promise<unknown>) {
    setBusy(true); setError("");
    try { await fn(); await load(); }
    catch { setError("Не удалось выполнить действие. Повторите попытку."); }
    finally { setBusy(false); }
  }
  return <main className="mx-auto max-w-3xl space-y-6 p-6">
    <h1 className="text-2xl font-semibold">Личный кабинет</h1>
    <p>{user.email}</p>
    <p role="status">{status}</p>
    <a href="/tg/" className="inline-block rounded bg-blue-600 px-4 py-2 text-white">Открыть Telegram</a>
    {user.role === "admin" && <a className="ml-4 text-blue-600 underline" href="/">Админ-панель</a>}
    <section className="space-y-3"><h2 className="text-lg font-semibold">Активные сессии</h2>
      <ul className="space-y-2">{sessions.map((s) => <li key={s.id} className="flex items-center justify-between gap-4 rounded border p-3">
        <span>{s.current ? "Это устройство" : "Другая сессия"} · до {new Date(s.expires_at).toLocaleString("ru-RU")}</span>
        <button disabled={busy} className="text-red-700 disabled:opacity-50" onClick={() => void action(async () => {
          await api.del(`/me/sessions/${encodeURIComponent(s.id)}`);
          if (s.current) window.location.assign("/");
        })}>Завершить</button>
      </li>)}</ul>
      <button disabled={busy} className="rounded border px-4 py-2" onClick={() => void action(async () => {
        await api.post("/me/sessions/revoke", {}); window.location.assign("/");
      })}>Завершить все сессии</button>
    </section>
    {features.telegram_bot && <section className="space-y-3"><h2 className="text-lg font-semibold">Привязка Telegram</h2>
      <p className="text-sm text-slate-600">Получите код и отправьте боту в личном чате команду /link с этим кодом. Код действует 15 минут.</p>
      <button disabled={busy} className="rounded border px-4 py-2" onClick={() => void action(async () => {
        const response = await api.post<{ code: string }>("/me/telegram-link", {}); setCode(response.code);
      })}>Получить код</button>
      {code && <p className="break-all rounded bg-slate-100 p-3 font-mono">/link {code}</p>}
    </section>}
    {error && <p role="alert" className="text-red-600">{error}</p>}
  </main>;
}
