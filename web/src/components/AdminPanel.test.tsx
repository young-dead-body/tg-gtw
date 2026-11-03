import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { AdminPanel } from "./AdminPanel";
import { mockFetch } from "../test/fetch-mock";
import type { User } from "../lib/types";

const admin: User = {
  id: "9",
  email: "admin@example.com",
  first_name: "Админ",
  last_name: "Админов",
  middle_name: "",
  telegram: "",
  department: "",
  position: "",
  role: "admin",
  is_active: true,
  blocked_at: "",
  expires_at: "",
};

const usersRoutes = [
  { method: "GET", path: "/api/dashboard", status: 200, body: { users_count: 3 } },
  {
    method: "GET",
    path: "/api/users",
    status: 200,
    body: { users: [admin] },
  },
  {
    method: "GET",
    path: "/api/registration-requests",
    status: 200,
    body: { requests: [] },
  },
  { method: "GET", path: "/api/stats", status: 200, body: { items: [], limit: 100, offset: 0 } },
  { method: "GET", path: "/api/logs", status: 200, body: { items: [], limit: 100, offset: 0 } },
  {
    method: "GET",
    path: "/api/settings/consent.document_version",
    status: 200,
    body: { key: "consent.document_version", value: "1.0" },
  },
];

describe("AdminPanel", () => {
  it("показывает дашборд с метриками по умолчанию", async () => {
    mockFetch(usersRoutes);
    await act(async () => {
      render(<AdminPanel admin={admin} onLoggedOut={vi.fn()} />);
    });

    expect(screen.getByText("Админ-панель")).toBeInTheDocument();
    expect(await screen.findByText("3")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Дашборд" })).toBeInTheDocument();
  });

  it("переключает вкладки и вызывает соответствующие эндпоинты", async () => {
    const fetchMock = mockFetch(usersRoutes);
    await act(async () => {
      render(<AdminPanel admin={admin} onLoggedOut={vi.fn()} />);
    });

    fireEvent.click(screen.getByRole("button", { name: "Пользователи" }));
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining("/api/users"),
        expect.anything(),
      );
    });
    expect(screen.getByText("Создать")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Логи" }));
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining("/api/logs"),
        expect.anything(),
      );
    });
    expect(screen.getByText("Аудит-лог")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Настройки" }));
    await waitFor(() => {
      expect(screen.getByText(/Версия документа согласия/)).toBeInTheDocument();
    });
  });

  it("вызывает /logout и сообщает о выходе", async () => {
    const fetchMock = mockFetch([
      ...usersRoutes,
      { method: "POST", path: "/api/logout", status: 200, body: { user: admin } },
    ]);
    const onLoggedOut = vi.fn();
    await act(async () => {
      render(<AdminPanel admin={admin} onLoggedOut={onLoggedOut} />);
    });

    fireEvent.click(screen.getByRole("button", { name: "Выйти" }));
    await waitFor(() => {
      expect(onLoggedOut).toHaveBeenCalledTimes(1);
    });
    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/logout"),
      expect.anything(),
    );
  });
});
