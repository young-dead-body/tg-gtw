import { api } from "./api";
import type {
  ConsentInfo,
  ConsentResponse,
  CreateUserPayload,
  DashboardData,
  LoginResponse,
  LogsResponse,
  MeResponse,
  RegistrationPayload,
  RegistrationResponse,
  RegistrationRequestsResponse,
  SettingResponse,
  StatsResponse,
  UpdateUserPayload,
  User,
  UsersResponse,
  Features,
} from "./types";

// --- auth & consent (клиентский флоу, S8) ---

export function getMe() {
  return api.get<MeResponse>("/me");
}

export function getConsentInfo() {
  return api.get<ConsentInfo>("/consent");
}

export function acceptConsent(documentVersion: string) {
  return api.post<ConsentResponse>("/consent", {
    accepted: true,
    document_version: documentVersion,
  });
}

export function login(email: string, password: string, headers?: Record<string, string>) {
  return api.post<LoginResponse>("/login", { email, password }, headers);
}

export function register(payload: RegistrationPayload) {
  return api.post<RegistrationResponse>("/registration", payload);
}

export function logout() {
  return api.post<{ user: User }>("/logout", {});
}

// --- admin: dashboard (T14.5) ---

export function getDashboard() {
  return api.get<DashboardData>("/dashboard");
}

// --- admin: users CRUD (T14.2) ---

export function listUsers() {
  return api.get<UsersResponse>("/users");
}

export function createUser(payload: CreateUserPayload) {
  return api.post<{ user: User }>("/users", payload);
}

export function updateUser(id: string, payload: UpdateUserPayload) {
  return api.patch<{ user: User }>(`/users/${id}`, payload);
}

export function deleteUser(id: string) {
  return api.del<{ user: User }>(`/users/${id}`);
}

// --- admin: registration requests (T14.2) ---

export function listRegistrationRequests() {
  return api.get<RegistrationRequestsResponse>("/registration-requests");
}

export function reviewRegistrationRequest(id: string, status: "approved" | "rejected") {
  return api.patch<{ request: { id: string; status: string } }>(
    `/registration-requests/${id}`,
    { status },
  );
}

// --- admin: statistics & logs (T14.3) ---

export function listStats(limit = 100, offset = 0) {
  return api.get<StatsResponse>(`/stats?limit=${limit}&offset=${offset}`);
}

export function listLogs(limit = 100, offset = 0, action = "") {
  const actionParam = action ? `&action=${encodeURIComponent(action)}` : "";
  return api.get<LogsResponse>(`/logs?limit=${limit}&offset=${offset}${actionParam}`);
}

// --- admin: settings (T14.1) ---

export function getSetting(key: string) {
  return api.get<SettingResponse>(`/settings/${encodeURIComponent(key)}`);
}

export function putSetting(key: string, value: string) {
  return api.put<SettingResponse>(`/settings/${encodeURIComponent(key)}`, { value });
}

// --- feature toggles (S11) ---

export function getFeatures() {
  return api.get<Features>("/features");
}
