import { useEffect, useState } from "react";
import { api, ApiClientError } from "../lib/api";

export function ResetPassword({ token }: { token: string }) {
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [done, setDone] = useState(false);
  useEffect(() => {
    // Bootstrap CSRF before the first anonymous POST.
    void api.get("/consent").catch(() => setError("Сервис недоступен. Повторите попытку позже."));
  }, []);
  async function submit(event: React.FormEvent) {
    event.preventDefault();
    if (password !== confirm) { setError("Пароли не совпадают"); return; }
    setBusy(true); setError("");
    try {
      await api.post("/password/reset", { token, password });
      setDone(true);
    } catch (err) {
      setError(err instanceof ApiClientError && err.status === 400
        ? "Ссылка недействительна или истекла. Запросите новую у бота."
        : err instanceof ApiClientError && err.status === 409
          ? "Этот пароль уже использовался. Выберите другой."
          : "Не удалось изменить пароль. Повторите попытку позже.");
    } finally { setBusy(false); }
  }
  return <main className="flex min-h-screen items-center justify-center bg-slate-50 p-4">
    <section className="w-full max-w-md rounded-xl bg-white p-6 shadow">
      <h1 className="text-xl font-semibold">Установка пароля</h1>
      {done ? <p className="mt-4">Пароль изменён, прежние сессии завершены. <a className="text-blue-600 underline" href="/">Войти</a></p>
        : <form onSubmit={(e) => void submit(e)} className="mt-4 space-y-4">
          <p className="text-sm text-slate-600">Ссылка действует 15 минут и используется один раз.</p>
          <label className="block">Новый пароль<input className="mt-1 w-full rounded border p-2" type="password" autoComplete="new-password" minLength={8} maxLength={1024} required value={password} onChange={(e) => setPassword(e.target.value)} /></label>
          <label className="block">Повторите пароль<input className="mt-1 w-full rounded border p-2" type="password" autoComplete="new-password" required value={confirm} onChange={(e) => setConfirm(e.target.value)} /></label>
          <button disabled={busy} className="rounded bg-blue-600 px-4 py-2 text-white disabled:opacity-50">{busy ? "Сохранение…" : "Установить пароль"}</button>
        </form>}
      {error && <p role="alert" className="mt-3 text-red-600">{error}</p>}
    </section>
  </main>;
}
