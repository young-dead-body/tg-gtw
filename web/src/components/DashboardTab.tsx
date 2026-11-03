import { useEffect, useState } from "react";
import { getDashboard } from "../lib/api-calls";
import type { DashboardData } from "../lib/types";

export function DashboardTab() {
  const [data, setData] = useState<DashboardData | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    getDashboard()
      .then((d) => {
        if (!cancelled) setData(d);
      })
      .catch(() => {
        if (!cancelled) setError("Не удалось загрузить метрики");
      });
    return () => {
      cancelled = true;
    };
  }, []);

  if (error) {
    return <p className="text-sm text-red-600">{error}</p>;
  }
  if (!data) {
    return <p className="text-sm text-slate-500">Загрузка...</p>;
  }

  const cards = [
    { label: "Пользователи", value: data.users_count },
    { label: "Заблокировано", value: data.blocked_users_count },
    { label: "Заявки на рассмотрении", value: data.pending_requests_count },
    { label: "Входов сегодня", value: data.logins_today },
    { label: "Входов всего", value: data.logins_total },
  ];

  return (
    <div className="grid grid-cols-2 gap-4 md:grid-cols-3 lg:grid-cols-5">
      {cards.map((card) => (
        <div key={card.label} className="rounded-lg border border-slate-200 bg-white p-5 shadow-sm">
          <p className="text-3xl font-semibold text-slate-900">{card.value}</p>
          <p className="mt-1 text-sm text-slate-500">{card.label}</p>
        </div>
      ))}
    </div>
  );
}
