import { act, fireEvent, render, screen } from "@testing-library/react";
import { LoginForm } from "./LoginForm";
import { mockFetch } from "../test/fetch-mock";

describe("LoginForm", () => {
  it("вызывает onSuccess при успешном входе", async () => {
    const onSuccess = vi.fn();
    mockFetch([
      {
        method: "POST",
        path: "/api/login",
        status: 200,
        body: {
          user: {
            id: "1",
            email: "alice@example.com",
            first_name: "Alice",
            last_name: "Smith",
            role: "user",
          },
        },
      },
    ]);

    render(<LoginForm onSuccess={onSuccess} onBack={vi.fn()} />);

    fireEvent.change(screen.getByLabelText("Email"), {
      target: { value: "alice@example.com" },
    });
    fireEvent.change(screen.getByLabelText("Пароль"), {
      target: { value: "secret123" },
    });

    await act(async () => {
      fireEvent.submit(screen.getByRole("button", { name: "Войти" }).closest("form")!);
    });

    expect(onSuccess).toHaveBeenCalledTimes(1);
  });

  it("передаёт consent_required из ответа логина", async () => {
    const onSuccess = vi.fn();
    mockFetch([
      {
        method: "POST",
        path: "/api/login",
        status: 200,
        body: {
          user: {
            id: "1",
            email: "alice@example.com",
            first_name: "Alice",
            last_name: "Smith",
            role: "user",
          },
          consent_required: true,
        },
      },
    ]);

    render(<LoginForm onSuccess={onSuccess} onBack={vi.fn()} />);

    fireEvent.change(screen.getByLabelText("Email"), {
      target: { value: "alice@example.com" },
    });
    fireEvent.change(screen.getByLabelText("Пароль"), {
      target: { value: "secret123" },
    });

    await act(async () => {
      fireEvent.submit(screen.getByRole("button", { name: "Войти" }).closest("form")!);
    });

    expect(onSuccess).toHaveBeenCalledWith(expect.objectContaining({ consent_required: true }));
  });

  it("показывает ошибку при неверных учётных данных", async () => {
    const onSuccess = vi.fn();
    mockFetch([
      { method: "POST", path: "/api/login", status: 401, body: { error: "invalid credentials" } },
    ]);

    render(<LoginForm onSuccess={onSuccess} onBack={vi.fn()} />);

    fireEvent.change(screen.getByLabelText("Email"), {
      target: { value: "alice@example.com" },
    });
    fireEvent.change(screen.getByLabelText("Пароль"), {
      target: { value: "wrong-password" },
    });

    await act(async () => {
      fireEvent.submit(screen.getByRole("button", { name: "Войти" }).closest("form")!);
    });

    expect(screen.getByRole("alert")).toHaveTextContent("Неверный email или пароль");
    expect(onSuccess).not.toHaveBeenCalled();
  });
});
