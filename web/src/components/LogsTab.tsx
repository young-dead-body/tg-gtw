import { useCallback, useEffect, useState } from "react";
import { listLogs } from "../lib/api-calls";
import { downloadCsv, printPage } from "../lib/export";
import type { LogItem } from "../lib/types";

const ACTIONS = [
  "login.success",
  "login.failed",
  "logout",
  "password.changed",
  "registration.requested",
  "registration.approved",
  "registration.rejected",
  "user.created",
  "user.updated",
  "user.deleted",
  "consent.accepted",
  "credentials.issued",
  "settings.updated",
];

export function LogsTab() {
  const [items, setItems] = useState<LogItem[]>([]);
  const [action, setAction] = useState("");
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setItems((await listLogs(100, 0, action)).items);
      setError(null);
    } catch {
      setError("Не удалось загрузить логи");
    }
  }, [action]);

  useEffect(() => {
    void load();
  }, [load]);

  const handleExport = () => {
    downloadCsv(
      "audit-log.csv",
      ["Время", "Пользователь", "Действие", "Сущность", "ID сущности", "IP", "User-Agent"],
      items.map((l) => [
        l.created_at,
        l.user_id ?? "",
        l.action,
        l.entity_type,
        l.entity_id,
        l.ip,
        l.user_agent,
      ]),
    );
  };

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h2 className="text-base font-semibold text-slate-900">Аудит-лог</h2>
        <div className="flex gap-2">
          <select
            value={action}
            onChange={(e) => setAction(e.target.value)}
            className="rounded-md border border-slate-300 px-3 py-1.5 text-sm"
          >
            <option value="">Все действия</option>
            {ACTIONS.map((a) => (
              <option key={a} value={a}>
                {a}
              </option>
            ))}
          </select>
          <button
            type="button"
            onClick={handleExport}
            className="rounded-md border border-slate-300 px-3 py-1.5 text-sm text-slate-700 hover:bg-slate-50"
          >
            Экспорт CSV
          </button>
          <button
            type="button"
            onClick={printPage}
            className="rounded-md border border-slate-300 px-3 py-1.5 text-sm text-slate-700 hover:bg-slate-50"
          >
            Печать (PDF)
          </button>
        </div>
      </div>

      {error && <p className="text-sm text-red-600">{error}</p>}

      <div className="overflow-x-auto rounded-lg border border-slate-200 bg-white">
        <table className="w-full text-left text-sm">
          <thead className="border-b border-slate-200 bg-slate-50 text-slate-600">
            <tr>
              <th className="px-4 py-2">Время</th>
              <th className="px-4 py-2">Пользователь</th>
              <th className="px-4 py-2">Действие</th>
              <th className="px-4 py-2">Сущность</th>
              <th className="px-4 py-2">IP</th>
              <th className="px-4 py-2">Детали</th>
            </tr>
          </thead>
          <tbody>
            {items.map((l) => (
              <tr key={l.id} className="border-b border-slate-100 last:border-0">
                <td className="px-4 py-2">{l.created_at}</td>
                <td className="px-4 py-2">{l.user_id ?? "—"}</td>
                <td className="px-4 py-2">{l.action}</td>
                <td className="px-4 py-2">
                  {l.entity_type}
                  {l.entity_id ? ` · ${l.entity_id}` : ""}
                </td>
                <td className="px-4 py-2">{l.ip || "—"}</td>
                <td className="px-4 py-2">{JSON.stringify(l.details)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
