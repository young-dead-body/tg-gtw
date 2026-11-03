import { redirectToLogin } from "./navigation";

const API_BASE = "/api";

// A 401 on these paths is an expected answer, not an expired session: /login
// reports bad credentials, /me is the boot-time session probe on the landing
// page (redirecting there would reload "/" forever).
const NO_LOGIN_REDIRECT = new Set(["/login", "/me"]);

// CSRF (T15.3): the server issues a double-submit tg_csrf cookie; every
// state-changing request must echo it in X-CSRF-Token.
const CSRF_COOKIE = "tg_csrf";

function readCSRFCookie(): string {
  const match = document.cookie
    .split("; ")
    .find((part) => part.startsWith(`${CSRF_COOKIE}=`));
  return match ? decodeURIComponent(match.slice(CSRF_COOKIE.length + 1)) : "";
}

function csrfHeaders(): Record<string, string> {
  const token = readCSRFCookie();
  return token ? { "X-CSRF-Token": token } : {};
}

export class ApiClientError extends Error {
  status: number;
  fieldErrors?: FieldError[];

  constructor(status: number, message: string, fieldErrors?: FieldError[]) {
    super(message);
    this.name = "ApiClientError";
    this.status = status;
    this.fieldErrors = fieldErrors;
  }
}

export interface FieldError {
  field: string;
  message: string;
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${API_BASE}${path}`, {
    credentials: "include",
    ...init,
  });

  if (res.status === 401 && !NO_LOGIN_REDIRECT.has(path)) {
    redirectToLogin();
  }

  if (!res.ok) {
    let message = "Ошибка запроса";
    let fieldErrors: FieldError[] | undefined;
    try {
      const body = await res.json();
      if (typeof body.error === "string" && body.error) {
        message = body.error;
      }
      if (Array.isArray(body.errors)) {
        fieldErrors = body.errors;
      }
    } catch {
      // non-JSON error body
    }
    throw new ApiClientError(res.status, message, fieldErrors);
  }

  return (await res.json()) as T;
}

function jsonBody(body: unknown, method: string, headers?: Record<string, string>): RequestInit {
  return {
    method,
    headers: { "Content-Type": "application/json", ...headers },
    body: JSON.stringify(body),
  };
}

export const api = {
  get: <T>(path: string) => request<T>(path),
  post: <T>(path: string, body: unknown, headers?: Record<string, string>) =>
    request<T>(path, jsonBody(body, "POST", { ...csrfHeaders(), ...headers })),
  put: <T>(path: string, body: unknown) => request<T>(path, jsonBody(body, "PUT", csrfHeaders())),
  patch: <T>(path: string, body: unknown) => request<T>(path, jsonBody(body, "PATCH", csrfHeaders())),
  del: <T>(path: string) => request<T>(path, { method: "DELETE", headers: csrfHeaders() }),
};
