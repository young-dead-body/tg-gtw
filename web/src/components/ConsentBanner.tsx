import { useState } from "react";
import { ApiClientError } from "../lib/api";
import { getConsentInfo, acceptConsent } from "../lib/api-calls";

interface ConsentBannerProps {
  documentVersion: string;
  onAccepted: (version: string) => void;
}

export function ConsentBanner({ documentVersion, onAccepted }: ConsentBannerProps) {
  const [version, setVersion] = useState(documentVersion);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleAccept = async () => {
    setSubmitting(true);
    setError(null);
    try {
      await acceptConsent(version);
      onAccepted(version);
    } catch (err) {
      if (err instanceof ApiClientError && err.status === 409) {
        try { setVersion((await getConsentInfo()).document_version); setError("Документ обновлён. Ознакомьтесь с новой версией и подтвердите согласие."); }
        catch { setError("Не удалось загрузить новую версию. Обновите страницу."); }
      } else setError("Не удалось сохранить согласие. Попробуйте ещё раз.");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div
      role="dialog"
      aria-label="Согласие на обработку персональных данных"
      className="fixed inset-x-0 bottom-0 z-50 border-t border-slate-200 bg-white p-4 shadow-lg sm:p-6"
    >
      <div className="mx-auto flex max-w-3xl flex-col gap-4 sm:flex-row sm:items-center">
        <p className="flex-1 text-sm leading-relaxed text-slate-600">
          Для использования сервиса необходимо согласие на обработку персональных данных
          (версия документа {version}). Согласие фиксируется: дата, IP-адрес,
          сведения о браузере.{" "}
          <a href="/api/documents/consent" target="_blank" rel="noopener noreferrer" className="text-blue-600 underline">Текст согласия</a>{" · "}
          <a href="/api/documents/privacy-policy" target="_blank" rel="noopener noreferrer" className="text-blue-600 underline">Политика обработки данных</a>
        </p>
        <button
          type="button"
          onClick={handleAccept}
          disabled={submitting}
          className="shrink-0 rounded-md bg-blue-600 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-blue-700 disabled:cursor-not-allowed disabled:opacity-60"
        >
          {submitting ? "Сохранение..." : "Принять"}
        </button>
      </div>
      {error && <p className="mx-auto mt-2 max-w-3xl text-sm text-red-600">{error}</p>}
    </div>
  );
}
