import { useState } from "react";
import type { ChangeEvent, FormEvent } from "react";
import { ApiClientError } from "../lib/api";
import { register } from "../lib/api-calls";
import { useFeatures } from "../lib/features";

const TELEGRAM_REGEXP = /^@?[a-zA-Z0-9_]{5,32}$/;
const EMAIL_REGEXP = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

interface FormValues {
  first_name: string;
  last_name: string;
  middle_name: string;
  email: string;
  telegram: string;
  department: string;
  position: string;
  reason: string;
}

const EMPTY_FORM: FormValues = {
  first_name: "",
  last_name: "",
  middle_name: "",
  email: "",
  telegram: "",
  department: "",
  position: "",
  reason: "",
};

interface RegisterFormProps {
  onBack: () => void;
}

export function RegisterForm({ onBack }: RegisterFormProps) {
  const { telegram_bot: telegramEnabled } = useFeatures();
  const [values, setValues] = useState<FormValues>(EMPTY_FORM);
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [submittedEmail, setSubmittedEmail] = useState<string | null>(null);

  const setField = (name: keyof FormValues) => (e: ChangeEvent<HTMLInputElement>) => {
    setValues((prev) => ({ ...prev, [name]: e.target.value }));
    setFieldErrors((prev) => {
      if (!prev[name]) return prev;
      const next = { ...prev };
      delete next[name];
      return next;
    });
  };

  const validate = (): Record<string, string> => {
    const errors: Record<string, string> = {};
    if (!values.first_name.trim()) errors.first_name = "Обязательное поле";
    if (!values.last_name.trim()) errors.last_name = "Обязательное поле";
    if (!values.email.trim()) errors.email = "Обязательное поле";
    else if (!EMAIL_REGEXP.test(values.email.trim())) errors.email = "Некорректный email";
    if (values.telegram.trim() && !TELEGRAM_REGEXP.test(values.telegram.trim())) {
      errors.telegram = "Некорректный telegram username";
    }
    if (!values.reason.trim()) errors.reason = "Обязательное поле";
    return errors;
  };

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    const errors = validate();
    setFieldErrors(errors);
    setFormError(null);
    if (Object.keys(errors).length > 0) return;

    setSubmitting(true);
    try {
      const payload: FormValues = {
        ...values,
        first_name: values.first_name.trim(),
        last_name: values.last_name.trim(),
        middle_name: values.middle_name.trim(),
        email: values.email.trim(),
        telegram: values.telegram.trim(),
        department: values.department.trim(),
        position: values.position.trim(),
        reason: values.reason.trim(),
      };
      const res = await register(payload);
      setSubmittedEmail(res.request.email);
    } catch (err) {
      if (err instanceof ApiClientError && err.fieldErrors) {
        const byField: Record<string, string> = {};
        for (const fe of err.fieldErrors) byField[fe.field] = fe.message;
        setFieldErrors(byField);
      } else if (err instanceof ApiClientError) {
        setFormError(err.message);
      } else {
        setFormError("Не удалось подключиться к серверу");
      }
    } finally {
      setSubmitting(false);
    }
  };

  if (submittedEmail) {
    return (
      <main className="flex min-h-screen items-center justify-center bg-slate-50 px-4">
        <div className="w-full max-w-md rounded-lg border border-slate-200 bg-white p-6 text-center shadow-sm">
          <h1 className="text-xl font-semibold text-slate-900">Заявка отправлена</h1>
          <p className="mt-3 text-sm leading-relaxed text-slate-600">
            Заявка на доступ для <span className="font-medium">{submittedEmail}</span>{" "}
            отправлена администратору. После одобрения вы сможете войти в систему.
          </p>
          <button
            type="button"
            onClick={onBack}
            className="mt-6 rounded-md bg-blue-600 px-6 py-2.5 text-sm font-medium text-white transition-colors hover:bg-blue-700"
          >
            На главную
          </button>
        </div>
      </main>
    );
  }

  return (
    <main className="flex min-h-screen items-center justify-center bg-slate-50 px-4 py-10">
      <div className="w-full max-w-lg rounded-lg border border-slate-200 bg-white p-6 shadow-sm">
        <h1 className="text-xl font-semibold text-slate-900">Регистрация</h1>
        <p className="mt-1 text-sm text-slate-500">
          Заполните заявку — администратор рассмотрит её вручную.
        </p>

        <form onSubmit={handleSubmit} className="mt-6 flex flex-col gap-4" noValidate>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field
              label="Имя"
              value={values.first_name}
              onChange={setField("first_name")}
              error={fieldErrors.first_name}
              autoComplete="given-name"
            />
            <Field
              label="Фамилия"
              value={values.last_name}
              onChange={setField("last_name")}
              error={fieldErrors.last_name}
              autoComplete="family-name"
            />
          </div>
          <Field
            label="Отчество"
            value={values.middle_name}
            onChange={setField("middle_name")}
            autoComplete="additional-name"
          />
          <Field
            label="Email"
            type="email"
            value={values.email}
            onChange={setField("email")}
            error={fieldErrors.email}
            autoComplete="email"
          />
          {telegramEnabled && (
            <Field
              label="Telegram"
              value={values.telegram}
              onChange={setField("telegram")}
              error={fieldErrors.telegram}
              placeholder="@username"
            />
          )}
          <div className="grid gap-4 sm:grid-cols-2">
            <Field
              label="Подразделение"
              value={values.department}
              onChange={setField("department")}
            />
            <Field label="Должность" value={values.position} onChange={setField("position")} />
          </div>
          <label className="flex flex-col gap-1.5">
            <span className="text-sm font-medium text-slate-700">
              Причина доступа <span className="text-red-600">*</span>
            </span>
            <textarea
              value={values.reason}
              onChange={(e) => {
                setValues((prev) => ({ ...prev, reason: e.target.value }));
                setFieldErrors((prev) => {
                  if (!prev.reason) return prev;
                  const next = { ...prev };
                  delete next.reason;
                  return next;
                });
              }}
              rows={3}
              className="rounded-md border border-slate-300 px-3 py-2 text-sm focus:border-blue-500 focus:outline-none"
            />
            {fieldErrors.reason && <span className="text-sm text-red-600">{fieldErrors.reason}</span>}
          </label>

          {formError && (
            <p role="alert" className="text-sm text-red-600">
              {formError}
            </p>
          )}

          <button
            type="submit"
            disabled={submitting}
            className="rounded-md bg-blue-600 px-4 py-2.5 text-sm font-medium text-white transition-colors hover:bg-blue-700 disabled:cursor-not-allowed disabled:opacity-60"
          >
            {submitting ? "Отправка..." : "Отправить заявку"}
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

interface FieldProps {
  label: string;
  value: string;
  onChange: (e: ChangeEvent<HTMLInputElement>) => void;
  error?: string;
  type?: string;
  placeholder?: string;
  autoComplete?: string;
}

function Field({ label, value, onChange, error, type = "text", placeholder, autoComplete }: FieldProps) {
  return (
    <label className="flex flex-col gap-1.5">
      <span className="text-sm font-medium text-slate-700">{label}</span>
      <input
        type={type}
        value={value}
        onChange={onChange}
        placeholder={placeholder}
        autoComplete={autoComplete}
        className="rounded-md border border-slate-300 px-3 py-2 text-sm focus:border-blue-500 focus:outline-none"
      />
      {error && <span className="text-sm text-red-600">{error}</span>}
    </label>
  );
}
