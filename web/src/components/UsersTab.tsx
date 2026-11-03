import { useCallback, useEffect, useState } from "react";
import { createUser, deleteUser, listUsers, updateUser } from "../lib/api-calls";
import { api } from "../lib/api";
import { downloadCsv, printPage } from "../lib/export";
import { useFeatures } from "../lib/features";
import type { CreateUserPayload, UpdateUserPayload, User } from "../lib/types";

interface UserFormState {
  first_name: string;
  last_name: string;
  middle_name: string;
  email: string;
  telegram: string;
  department: string;
  position: string;
  role: string;
  password: string;
  expires_at: string;
}

const emptyForm: UserFormState = {
  first_name: "",
  last_name: "",
  middle_name: "",
  email: "",
  telegram: "",
  department: "",
  position: "",
  role: "user",
  password: "",
  expires_at: "",
};

export function UsersTab() {
  const { telegram_bot: telegramEnabled } = useFeatures();
  const [users, setUsers] = useState<User[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [form, setForm] = useState<UserFormState | null>(null);
  const [editing, setEditing] = useState<User | null>(null);
  const [linkCode, setLinkCode] = useState<{ email: string; code: string } | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      setUsers((await listUsers()).users);
      setError(null);
    } catch {
      setError("Не удалось загрузить пользователей");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const openCreate = () => {
    setForm({ ...emptyForm });
    setEditing(null);
  };

  const openEdit = (user: User) => {
    setForm({
      first_name: user.first_name,
      last_name: user.last_name,
      middle_name: user.middle_name,
      email: user.email,
      telegram: user.telegram,
      department: user.department,
      position: user.position,
      role: user.role,
      password: "",
      expires_at: user.expires_at,
    });
    setEditing(user);
  };

  const close = () => {
    setForm(null);
    setEditing(null);
  };

  const handleSubmit = async () => {
    if (!form) return;
    setBusy(true);
    setError(null);
    try {
      if (editing) {
        const payload: UpdateUserPayload = {
          first_name: form.first_name,
          last_name: form.last_name,
          middle_name: form.middle_name,
          telegram: form.telegram,
          department: form.department,
          position: form.position,
          role: form.role,
          expires_at: form.expires_at || undefined,
        };
        if (form.password) payload.password = form.password;
        await updateUser(editing.id, payload);
      } else {
        const payload: CreateUserPayload = {
          first_name: form.first_name,
          last_name: form.last_name,
          middle_name: form.middle_name,
          email: form.email,
          telegram: form.telegram,
          department: form.department,
          position: form.position,
          role: form.role,
          expires_at: form.expires_at || undefined,
        };
        if (form.password) payload.password = form.password;
        await createUser(payload);
      }
      close();
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Не удалось сохранить пользователя");
    } finally {
      setBusy(false);
    }
  };

  const handleToggleBlock = async (user: User) => {
    setBusy(true);
    setError(null);
    try {
      await updateUser(user.id, { blocked: !user.blocked_at });
      await load();
    } catch {
      setError("Не удалось изменить статус пользователя");
    } finally {
      setBusy(false);
    }
  };

  const handleDelete = async (user: User) => {
    if (!window.confirm(`Удалить пользователя ${user.email}?`)) return;
    setBusy(true);
    setError(null);
    try {
      await deleteUser(user.id);
      await load();
    } catch {
      setError("Не удалось удалить пользователя");
    } finally {
      setBusy(false);
    }
  };

  const handleExport = () => {
    const headers = telegramEnabled
      ? ["Email", "ФИО", "Telegram", "Отдел", "Должность", "Роль", "Активен", "Заблокирован"]
      : ["Email", "ФИО", "Отдел", "Должность", "Роль", "Активен", "Заблокирован"];
    const rows = users.map((u) => {
      const row = [
        u.email,
        `${u.last_name} ${u.first_name} ${u.middle_name}`.trim(),
        u.department,
        u.position,
        u.role,
        u.is_active ? "да" : "нет",
        u.blocked_at ? "да" : "нет",
      ];
      if (telegramEnabled) {
        row.splice(2, 0, u.telegram);
      }
      return row;
    });
    downloadCsv("users.csv", headers, rows);
  };

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h2 className="text-base font-semibold text-slate-900">Пользователи</h2>
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
          <button
            type="button"
            onClick={openCreate}
            className="rounded-md bg-blue-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-blue-700"
          >
            Создать
          </button>
        </div>
      </div>

      {error && <p className="text-sm text-red-600">{error}</p>}
      {linkCode && <div className="rounded border border-blue-200 bg-blue-50 p-4">
        <p>Код привязки для {linkCode.email} действует 15 минут. Передайте его сотруднику лично: в личном чате с ботом нужно отправить эту команду.</p>
        <p className="mt-2 break-all font-mono">/link {linkCode.code}</p>
        <button type="button" className="mt-2 text-blue-700 underline" onClick={() => setLinkCode(null)}>Скрыть код</button>
      </div>}

      <div className="overflow-x-auto rounded-lg border border-slate-200 bg-white">
        <table className="w-full text-left text-sm">
          <thead className="border-b border-slate-200 bg-slate-50 text-slate-600">
            <tr>
              <th className="px-4 py-2">Email</th>
              <th className="px-4 py-2">ФИО</th>
              {telegramEnabled && <th className="px-4 py-2">Telegram</th>}
              <th className="px-4 py-2">Роль</th>
              <th className="px-4 py-2">Статус</th>
              <th className="px-4 py-2">Срок доступа</th>
              <th className="px-4 py-2">Действия</th>
            </tr>
          </thead>
          <tbody>
            {users.map((u) => (
              <tr key={u.id} className="border-b border-slate-100 last:border-0">
                <td className="px-4 py-2">{u.email}</td>
                <td className="px-4 py-2">
                  {u.last_name} {u.first_name}
                </td>
                {telegramEnabled && <td className="px-4 py-2">{u.telegram || "—"}</td>}
                <td className="px-4 py-2">{u.role}</td>
                <td className="px-4 py-2">
                  {u.blocked_at ? (
                    <span className="text-red-600">заблокирован</span>
                  ) : u.is_active ? (
                    <span className="text-green-600">активен</span>
                  ) : (
                    <span className="text-slate-500">отключён</span>
                  )}
                </td>
                <td className="px-4 py-2">{u.expires_at || "—"}</td>
                <td className="px-4 py-2">
                  <div className="flex gap-2">
                    <button
                      type="button"
                      onClick={() => openEdit(u)}
                      className="text-blue-600 hover:underline"
                    >
                      Изменить
                    </button>
                    <button
                      type="button"
                      onClick={() => void handleToggleBlock(u)}
                      className="text-amber-600 hover:underline"
                    >
                      {u.blocked_at ? "Разблокировать" : "Заблокировать"}
                    </button>
                    {telegramEnabled && <button type="button" disabled={busy || !u.is_active} className="text-blue-700 disabled:opacity-50" onClick={() => {
                      setBusy(true); setError(null);
                      void api.post<{ code: string }>(`/users/${u.id}/telegram-link`, {})
                        .then((result) => setLinkCode({ email: u.email, code: result.code }))
                        .catch(() => setError("Не удалось получить код привязки"))
                        .finally(() => setBusy(false));
                    }}>Привязать Telegram</button>}
                    <button
                      type="button"
                      onClick={() => void handleDelete(u)}
                      className="text-red-600 hover:underline"
                    >
                      Удалить
                    </button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {form && (
        <div className="fixed inset-0 flex items-center justify-center bg-black/40 p-4">
          <div className="w-full max-w-lg rounded-lg bg-white p-6 shadow-lg">
            <h3 className="text-base font-semibold text-slate-900">
              {editing ? "Редактирование пользователя" : "Новый пользователь"}
            </h3>
            <form
              className="mt-4 grid grid-cols-2 gap-3"
              onSubmit={(e) => {
                e.preventDefault();
                void handleSubmit();
              }}
            >
              <Field label="Имя" value={form.first_name} onChange={(v) => setForm({ ...form, first_name: v })} />
              <Field label="Фамилия" value={form.last_name} onChange={(v) => setForm({ ...form, last_name: v })} />
              <Field label="Отчество" value={form.middle_name} onChange={(v) => setForm({ ...form, middle_name: v })} />
              <Field
                label="Email"
                value={form.email}
                onChange={(v) => setForm({ ...form, email: v })}
                disabled={!!editing}
              />
              {telegramEnabled && (
                <Field label="Telegram" value={form.telegram} onChange={(v) => setForm({ ...form, telegram: v })} />
              )}
              <Field label="Отдел" value={form.department} onChange={(v) => setForm({ ...form, department: v })} />
              <Field label="Должность" value={form.position} onChange={(v) => setForm({ ...form, position: v })} />
              <label className="flex flex-col gap-1">
                <span className="text-sm text-slate-600">Роль</span>
                <select
                  value={form.role}
                  onChange={(e) => setForm({ ...form, role: e.target.value })}
                  className="rounded-md border border-slate-300 px-3 py-2 text-sm"
                >
                  <option value="user">user</option>
                  <option value="admin">admin</option>
                </select>
              </label>
              <Field
                label={editing ? "Новый пароль (необязательно)" : "Пароль (необязательно)"}
                value={form.password}
                onChange={(v) => setForm({ ...form, password: v })}
                type="password"
              />
              <Field
                label="Срок доступа (YYYY-MM-DDTHH:MM:SSZ)"
                value={form.expires_at}
                onChange={(v) => setForm({ ...form, expires_at: v })}
              />
              <div className="col-span-2 mt-2 flex justify-end gap-2">
                <button
                  type="button"
                  onClick={close}
                  className="rounded-md border border-slate-300 px-4 py-2 text-sm text-slate-700 hover:bg-slate-50"
                >
                  Отмена
                </button>
                <button
                  type="submit"
                  disabled={busy}
                  className="rounded-md bg-blue-600 px-4 py-2 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-60"
                >
                  {busy ? "Сохранение..." : "Сохранить"}
                </button>
              </div>
            </form>
          </div>
        </div>
      )}
    </div>
  );
}

interface FieldProps {
  label: string;
  value: string;
  onChange: (value: string) => void;
  type?: string;
  disabled?: boolean;
}

function Field({ label, value, onChange, type = "text", disabled }: FieldProps) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-sm text-slate-600">{label}</span>
      <input
        type={type}
        value={value}
        disabled={disabled}
        onChange={(e) => onChange(e.target.value)}
        className="rounded-md border border-slate-300 px-3 py-2 text-sm disabled:bg-slate-100"
      />
    </label>
  );
}
