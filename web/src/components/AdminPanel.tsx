import { useState } from "react";
import type { User } from "../lib/types";
import { logout } from "../lib/api-calls";
import { redirectToTg } from "../lib/navigation";
import { DashboardTab } from "./DashboardTab";
import { UsersTab } from "./UsersTab";
import { RequestsTab } from "./RequestsTab";
import { StatsTab } from "./StatsTab";
import { LogsTab } from "./LogsTab";
import { SettingsTab } from "./SettingsTab";

export type AdminTab =
  | "dashboard"
  | "users"
  | "requests"
  | "stats"
  | "logs"
  | "settings";

const TABS: { id: AdminTab; label: string }[] = [
  { id: "dashboard", label: "Дашборд" },
  { id: "users", label: "Пользователи" },
  { id: "requests", label: "Заявки" },
  { id: "stats", label: "Статистика" },
  { id: "logs", label: "Логи" },
  { id: "settings", label: "Настройки" },
];

interface AdminPanelProps {
  admin: User;
  onLoggedOut: () => void;
}

export function AdminPanel({ admin, onLoggedOut }: AdminPanelProps) {
  const [tab, setTab] = useState<AdminTab>("dashboard");
  const [busy, setBusy] = useState(false);

  const handleLogout = async () => {
    setBusy(true);
    try {
      await logout();
    } catch {
      // выход в любом случае
    } finally {
      onLoggedOut();
    }
  };

  return (
    <main className="min-h-screen bg-slate-100">
      <header className="flex items-center justify-between border-b border-slate-200 bg-white px-6 py-3">
        <div>
          <h1 className="text-lg font-semibold text-slate-900">Админ-панель</h1>
          <p className="text-sm text-slate-500">
            {admin.first_name} {admin.last_name} · {admin.email}
          </p>
        </div>
        <div className="flex items-center gap-2">
 <a href="/?account=1" className="text-sm text-blue-600 underline">Личный кабинет</a>
          <button
            type="button"
            onClick={redirectToTg}
            className="rounded-md border border-slate-300 px-3 py-1.5 text-sm text-slate-700 hover:bg-slate-50"
          >
            В рабочее пространство
          </button>
          <button
            type="button"
            onClick={handleLogout}
            disabled={busy}
            className="rounded-md bg-slate-800 px-3 py-1.5 text-sm font-medium text-white hover:bg-slate-700 disabled:opacity-60"
          >
            {busy ? "Выход..." : "Выйти"}
          </button>
        </div>
      </header>

      <div className="flex">
        <nav className="w-52 shrink-0 border-r border-slate-200 bg-white p-3">
          <ul className="flex flex-col gap-1">
            {TABS.map(({ id, label }) => (
              <li key={id}>
                <button
                  type="button"
                  onClick={() => setTab(id)}
                  className={`w-full rounded-md px-3 py-2 text-left text-sm ${
                    tab === id
                      ? "bg-blue-600 font-medium text-white"
                      : "text-slate-700 hover:bg-slate-100"
                  }`}
                >
                  {label}
                </button>
              </li>
            ))}
          </ul>
        </nav>

        <section className="flex-1 p-6">
          {tab === "dashboard" && <DashboardTab />}
          {tab === "users" && <UsersTab />}
          {tab === "requests" && <RequestsTab />}
          {tab === "stats" && <StatsTab />}
          {tab === "logs" && <LogsTab />}
          {tab === "settings" && <SettingsTab />}
        </section>
      </div>
    </main>
  );
}
