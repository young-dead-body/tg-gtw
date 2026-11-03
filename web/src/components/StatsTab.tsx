import { useCallback, useEffect, useState } from "react";
import { listStats } from "../lib/api-calls";
import { downloadCsv, printPage } from "../lib/export";
import type { StatsItem } from "../lib/types";

export function StatsTab() {
  const [items, setItems] = useState<StatsItem[]>([]);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setItems((await listStats()).items);
      setError(null);
    } catch {
      setError("Не удалось загрузить статистику");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const handleExport = () => {
    downloadCsv(
      "statistics.csv",
      ["Время входа", "Пользователь", "IP", "Страна", "Город", "Браузер", "ОС", "Устройство", "Экран", "Ошибка"],
      items.map((s) => [
        s.login_at,
        s.user_id,
        s.ip,
        s.country,
        s.city,
        s.browser,
        s.os,
        s.device,
        s.screen_resolution,
        s.auth_error,
      ]),
    );
  };

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h2 className="text-base font-semibold text-slate-900">Статистика входов</h2>
        <div className="flex gap-2">
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
              <th className="px-4 py-2">IP</th>
              <th className="px-4 py-2">Гео</th>
              <th className="px-4 py-2">Браузер</th>
              <th className="px-4 py-2">ОС</th>
              <th className="px-4 py-2">Устройство</th>
              <th className="px-4 py-2">Экран</th>
              <th className="px-4 py-2">Результат</th>
            </tr>
          </thead>
          <tbody>
            {items.map((s) => (
              <tr key={s.id} className="border-b border-slate-100 last:border-0">
                <td className="px-4 py-2">{s.login_at}</td>
                <td className="px-4 py-2">{s.user_id}</td>
                <td className="px-4 py-2">{s.ip}</td>
                <td className="px-4 py-2">
                  {[s.country, s.city].filter(Boolean).join(" · ") || "—"}
                </td>
                <td className="px-4 py-2">{s.browser || "—"}</td>
                <td className="px-4 py-2">{s.os || "—"}</td>
                <td className="px-4 py-2">{s.device || "—"}</td>
                <td className="px-4 py-2">{s.screen_resolution || "—"}</td>
                <td className="px-4 py-2">
                  {s.auth_error ? (
                    <span className="text-red-600">{s.auth_error}</span>
                  ) : (
                    <span className="text-green-600">успешно</span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
