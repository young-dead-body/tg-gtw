import { useState } from "react";
import type { FormEvent } from "react";
import { ApiClientError } from "../lib/api";
import { login } from "../lib/api-calls";
import type { LoginResponse } from "../lib/types";

const LOGIN_ERROR_MESSAGES: Record<string, string> = {
  "invalid credentials": "Неверный email или пароль",
  "account is disabled": "Учётная запись отключена",
  "account is blocked": "Учётная запись заблокирована",
  "access expired": "Срок доступа истёк",
  password_expired: "Пароль истёк. Обратитесь к администратору",
};

interface LoginFormProps {
  onSuccess: (result: LoginResponse) => void;
  onBack: () => void;
}

export function LoginForm({ onSuccess, onBack }: LoginFormProps) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      // Разрешение экрана передаётся для статистики входа (ТЗ §12).
      const screen = `${window.screen.width}x${window.screen.height}`;
      const res = await login(email.trim(), password, { "X-Screen-Resolution": screen });
      onSuccess(res);
    } catch (err) {
      if (err instanceof ApiClientError) {
        setError(LOGIN_ERROR_MESSAGES[err.message] ?? err.message);
      } else {
        setError("Не удалось подключиться к серверу");
      }
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <main className="flex min-h-screen items-center justify-center bg-slate-50 px-4">
      <div className="w-full max-w-sm rounded-lg border border-slate-200 bg-white p-6 shadow-sm">
        <h1 className="text-xl font-semibold text-slate-900">Вход</h1>
        <p className="mt-1 text-sm text-slate-500">
          После входа вы будете перенаправлены в рабочее пространство.
        </p>

        <form onSubmit={handleSubmit} className="mt-6 flex flex-col gap-4" noValidate>
          <label className="flex flex-col gap-1.5">
            <span className="text-sm font-medium text-slate-700">Email</span>
            <input
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              autoComplete="username"
              required
              className="rounded-md border border-slate-300 px-3 py-2 text-sm focus:border-blue-500 focus:outline-none"
            />
          </label>
          <label className="flex flex-col gap-1.5">
            <span className="text-sm font-medium text-slate-700">Пароль</span>
            <input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="current-password"
              required
              className="rounded-md border border-slate-300 px-3 py-2 text-sm focus:border-blue-500 focus:outline-none"
            />
          </label>

          {error && (
            <p role="alert" className="text-sm text-red-600">
              {error}
            </p>
          )}

          <button
            type="submit"
            disabled={submitting}
            className="rounded-md bg-blue-600 px-4 py-2.5 text-sm font-medium text-white transition-colors hover:bg-blue-700 disabled:cursor-not-allowed disabled:opacity-60"
          >
            {submitting ? "Вход..." : "Войти"}
          </button>
        </form>

        <button
          type="button"
          onClick={onBack}
          className="mt-4 text-sm text-slate-500 hover:text-slate-700"
        >
          ← На главную
        </button>
      </div>
    </main>
  );
}
