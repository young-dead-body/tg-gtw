import { api, ApiClientError } from "./api";
import { redirectToLogin } from "./navigation";
import { mockFetch } from "../test/fetch-mock";

vi.mock("./navigation", () => ({
  redirectToLogin: vi.fn(),
}));

const mockRedirect = vi.mocked(redirectToLogin);

describe("api", () => {
  beforeEach(() => {
    mockRedirect.mockClear();
  });

  it("перенаправляет на страницу входа при 401", async () => {
    mockFetch([{ method: "GET", path: "/api/users", status: 401, body: { error: "unauthorized" } }]);

    await expect(api.get("/users")).rejects.toBeInstanceOf(ApiClientError);
    expect(mockRedirect).toHaveBeenCalledTimes(1);
  });

  it("не перенаправляет при 401 на /login и /me", async () => {
    mockFetch([
      { method: "POST", path: "/api/login", status: 401, body: { error: "invalid credentials" } },
      { method: "GET", path: "/api/me", status: 401, body: { error: "unauthorized" } },
    ]);

    await expect(api.post("/login", {})).rejects.toBeInstanceOf(ApiClientError);
    await expect(api.get("/me")).rejects.toBeInstanceOf(ApiClientError);
    expect(mockRedirect).not.toHaveBeenCalled();
  });

  it("не перенаправляет при других ошибках", async () => {
    mockFetch([
      { method: "GET", path: "/api/users", status: 403, body: { error: "forbidden" } },
      { method: "GET", path: "/api/stats", status: 500, body: { error: "boom" } },
    ]);

    await expect(api.get("/users")).rejects.toBeInstanceOf(ApiClientError);
    await expect(api.get("/stats")).rejects.toBeInstanceOf(ApiClientError);
    expect(mockRedirect).not.toHaveBeenCalled();
  });
});
