import { act, fireEvent, render, screen } from "@testing-library/react";
import { RegisterForm } from "./RegisterForm";
import { mockFetch } from "../test/fetch-mock";

describe("RegisterForm", () => {
  it("валидирует обязательные поля до отправки", async () => {
    const fetchMock = mockFetch([]);

    render(<RegisterForm onBack={vi.fn()} />);

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Отправить заявку" }));
    });

    expect(screen.getAllByText("Обязательное поле")).toHaveLength(4);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("отправляет заявку и показывает экран успеха", async () => {
    const fetchMock = mockFetch([
      {
        method: "POST",
        path: "/api/registration",
        status: 201,
        body: {
          request: {
            id: "1",
            email: "ivan@example.com",
            status: "pending",
            created_at: "2026-01-01T00:00:00Z",
          },
        },
      },
    ]);

    render(<RegisterForm onBack={vi.fn()} />);

    fireEvent.change(screen.getByLabelText("Имя"), { target: { value: "Иван" } });
    fireEvent.change(screen.getByLabelText("Фамилия"), { target: { value: "Иванов" } });
    fireEvent.change(screen.getByLabelText("Отчество"), { target: { value: "Иванович" } });
    fireEvent.change(screen.getByLabelText("Email"), {
      target: { value: "ivan@example.com" },
    });
    fireEvent.change(screen.getByLabelText("Telegram"), {
      target: { value: "@ivanov" },
    });
    fireEvent.change(screen.getByLabelText("Подразделение"), {
      target: { value: "ИТ" },
    });
    fireEvent.change(screen.getByLabelText("Должность"), {
      target: { value: "Разработчик" },
    });
    fireEvent.change(screen.getByLabelText("Причина доступа", { exact: false }), {
      target: { value: "Работа с клиентами" },
    });

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Отправить заявку" }));
    });

    expect(
      screen.getByRole("heading", { name: "Заявка отправлена" }),
    ).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(1);

    const [, init] = fetchMock.mock.calls[0];
    const body = JSON.parse(init?.body as string) as Record<string, string>;
    expect(body.email).toBe("ivan@example.com");
    expect(body.telegram).toBe("@ivanov");
    expect(body.reason).toBe("Работа с клиентами");
  });
});
