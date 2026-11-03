import { useCallback, useEffect, useState } from "react";
import { getSetting, putSetting } from "../lib/api-calls";

const CONSENT_SETTING = "consent.document_version";

export function SettingsTab() {
  const [consentVersion, setConsentVersion] = useState("");
  const [message, setMessage] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const setting = await getSetting(CONSENT_SETTING);
      setConsentVersion(setting.value);
      setError(null);
    } catch {
      // настройка ещё не задана — дефолт 1.0
      setConsentVersion("1.0");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const handleSave = async () => {
    if (!consentVersion.trim()) {
      setError("Версия не может быть пустой");
      return;
    }
    setBusy(true);
    setError(null);
    setMessage(null);
    try {
      await putSetting(CONSENT_SETTING, consentVersion.trim());
      setMessage("Сохранено");
    } catch {
      setError("Не удалось сохранить настройку");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="max-w-md space-y-4">
      <h2 className="text-base font-semibold text-slate-900">Настройки</h2>

      <div className="rounded-lg border border-slate-200 bg-white p-5 shadow-sm">
        <label className="flex flex-col gap-1.5">
          <span className="text-sm font-medium text-slate-700">
            Версия документа согласия (consent.document_version)
          </span>
          <input
            type="text"
            value={consentVersion}
            onChange={(e) => setConsentVersion(e.target.value)}
            className="rounded-md border border-slate-300 px-3 py-2 text-sm focus:border-blue-500 focus:outline-none"
          />
          <span className="text-xs text-slate-500">
            При смене версии пользователям снова потребуется принять согласие.
          </span>
        </label>

        {error && <p className="mt-3 text-sm text-red-600">{error}</p>}
        {message && <p className="mt-3 text-sm text-green-600">{message}</p>}

        <button
          type="button"
          onClick={() => void handleSave()}
          disabled={busy}
          className="mt-4 rounded-md bg-blue-600 px-4 py-2 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-60"
        >
          {busy ? "Сохранение..." : "Сохранить"}
        </button>
      </div>
    </div>
  );
}
