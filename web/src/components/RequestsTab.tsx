import { useCallback, useEffect, useState } from "react";
import { listRegistrationRequests, reviewRegistrationRequest } from "../lib/api-calls";
import { useFeatures } from "../lib/features";
import type { RegistrationRequestItem } from "../lib/types";

export function RequestsTab() {
  const { telegram_bot: telegramEnabled } = useFeatures();
  const [requests, setRequests] = useState<RegistrationRequestItem[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      setRequests((await listRegistrationRequests()).requests);
      setError(null);
    } catch {
      setError("Не удалось загрузить заявки");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const handleReview = async (request: RegistrationRequestItem, status: "approved" | "rejected") => {
    if (status === "rejected" && !window.confirm(`Отклонить заявку ${request.email}?`)) return;
    setBusy(true);
    setError(null);
    try {
      await reviewRegistrationRequest(request.id, status);
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Не удалось обработать заявку");
    } finally {
      setBusy(false);
    }
  };

  const pending = requests.filter((r) => r.status === "pending");

  return (
    <div className="space-y-4">
      <h2 className="text-base font-semibold text-slate-900">
        Заявки на доступ {pending.length > 0 && `(${pending.length})`}
      </h2>

      {error && <p className="text-sm text-red-600">{error}</p>}

      <div className="overflow-x-auto rounded-lg border border-slate-200 bg-white">
        <table className="w-full text-left text-sm">
          <thead className="border-b border-slate-200 bg-slate-50 text-slate-600">
            <tr>
              <th className="px-4 py-2">ФИО</th>
              <th className="px-4 py-2">Email</th>
              {telegramEnabled && <th className="px-4 py-2">Telegram</th>}
              <th className="px-4 py-2">Отдел / Должность</th>
              <th className="px-4 py-2">Причина</th>
              <th className="px-4 py-2">Статус</th>
              <th className="px-4 py-2">Действия</th>
            </tr>
          </thead>
          <tbody>
            {requests.map((r) => (
              <tr key={r.id} className="border-b border-slate-100 last:border-0">
                <td className="px-4 py-2">
                  {r.last_name} {r.first_name}
                </td>
                <td className="px-4 py-2">{r.email}</td>
                {telegramEnabled && <td className="px-4 py-2">{r.telegram || "—"}</td>}
                <td className="px-4 py-2">
                  {[r.department, r.position].filter(Boolean).join(" / ") || "—"}
                </td>
                <td className="px-4 py-2">{r.reason || "—"}</td>
                <td className="px-4 py-2">
                  {r.status === "pending" ? (
                    <span className="text-amber-600">на рассмотрении</span>
                  ) : r.status === "approved" ? (
                    <span className="text-green-600">одобрена</span>
                  ) : (
                    <span className="text-red-600">отклонена</span>
                  )}
                </td>
                <td className="px-4 py-2">
                  {r.status === "pending" ? (
                    <div className="flex gap-2">
                      <button
                        type="button"
                        disabled={busy}
                        onClick={() => void handleReview(r, "approved")}
                        className="text-green-600 hover:underline disabled:opacity-60"
                      >
                        Одобрить
                      </button>
                      <button
                        type="button"
                        disabled={busy}
                        onClick={() => void handleReview(r, "rejected")}
                        className="text-red-600 hover:underline disabled:opacity-60"
                      >
                        Отклонить
                      </button>
                    </div>
                  ) : (
                    "—"
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
