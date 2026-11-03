import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { App } from "./App";
import { mockFetch } from "./test/fetch-mock";
import { redirectToTg } from "./lib/navigation";

vi.mock("./lib/navigation", () => ({
  redirectToTg: vi.fn(),
  redirectToLogin: vi.fn(),
}));

const mockRedirect = vi.mocked(redirectToTg);

const user = {
  id: "1",
  email: "alice@example.com",
  first_name: "Alice",
  last_name: "Smith",
  role: "user",
};

describe("App", () => {
  beforeEach(() => {
    mockRedirect.mockClear();
  });

  it("скрывает кнопки входа до принятия согласия", async () => {
    mockFetch([
      { method: "GET", path: "/api/me", status: 401, body: { error: "unauthorized" } },
      {
        method: "GET",
        path: "/api/consent",
        status: 200,
        body: { document_version: "1.0", required: true },
      },
      {
        method: "POST",
        path: "/api/consent",
        status: 201,
        body: { document_version: "1.0", accepted_at: "2026-01-01T00:00:00Z" },
      },
    ]);

    await act(async () => {
      render(<App />);
    });

    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Войти" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Регистрация" })).not.toBeInTheDocument();

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Принять" }));
    });

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Войти" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Регистрация" })).toBeInTheDocument();
  });

  it("перенаправляет авторизованного пользователя в /tg/", async () => {
    mockFetch([{ method: "GET", path: "/api/me", status: 200, body: { user } }]);

    await act(async () => {
      render(<App />);
    });

    expect(mockRedirect).toHaveBeenCalledTimes(1);
  });

  it("показывает согласие авторизованному пользователю без записи consent и уводит в /tg/ после принятия", async () => {
    mockFetch([
      { method: "GET", path: "/api/me", status: 200, body: { user, consent_required: true } },
      {
        method: "GET",
        path: "/api/consent",
        status: 200,
        body: { document_version: "1.0", required: true },
      },
      {
        method: "POST",
        path: "/api/consent",
        status: 201,
        body: { document_version: "1.0", accepted_at: "2026-01-01T00:00:00Z" },
      },
    ]);

    await act(async () => {
      render(<App />);
    });

    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(mockRedirect).not.toHaveBeenCalled();
    // кнопки входа скрыты — пользователь уже авторизован
    expect(screen.queryByRole("button", { name: "Войти" })).not.toBeInTheDocument();

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Принять" }));
    });

    expect(mockRedirect).toHaveBeenCalledTimes(1);
  });

  it("показывает админ-панель для пользователя с ролью admin", async () => {
    const adminUser = { ...user, id: "9", email: "admin@example.com", role: "admin" };
    mockFetch([
      { method: "GET", path: "/api/me", status: 200, body: { user: adminUser } },
      { method: "GET", path: "/api/dashboard", status: 200, body: { users_count: 5 } },
    ]);

    await act(async () => {
      render(<App />);
    });

    expect(await screen.findByText("Админ-панель")).toBeInTheDocument();
    expect(mockRedirect).not.toHaveBeenCalled();
  });

  it("не требует повторного согласия для уже принятой версии документа", async () => {
    window.localStorage.setItem("tg_consent_accepted", "1.0");
    mockFetch([
      { method: "GET", path: "/api/me", status: 401, body: { error: "unauthorized" } },
      {
        method: "GET",
        path: "/api/consent",
        status: 200,
        body: { document_version: "1.0", required: true },
      },
    ]);

    await act(async () => {
      render(<App />);
    });

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Войти" })).toBeInTheDocument();
    });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});
