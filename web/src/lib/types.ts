export interface User {
  id: string;
  email: string;
  first_name: string;
  last_name: string;
  middle_name: string;
  telegram: string;
  department: string;
  position: string;
  role: string;
  is_active: boolean;
  blocked_at: string;
  expires_at: string;
}

export interface ConsentInfo {
  document_version: string;
  required: boolean;
}

export interface FieldError {
  field: string;
  message: string;
}

export interface MeResponse {
  user: User;
  consent_required?: boolean;
}

export interface LoginResponse {
  user: User;
  consent_required?: boolean;
}

export interface ConsentResponse {
  document_version: string;
  accepted_at: string;
}

export interface RegistrationPayload {
  first_name: string;
  last_name: string;
  middle_name: string;
  email: string;
  telegram: string;
  department: string;
  position: string;
  reason: string;
}

export interface RegistrationResponse {
  request: {
    id: string;
    email: string;
    status: string;
    created_at: string;
  };
}

export interface UsersResponse {
  users: User[];
}

export interface CreateUserPayload {
  first_name: string;
  last_name: string;
  middle_name?: string;
  email: string;
  telegram?: string;
  department?: string;
  position?: string;
  role?: string;
  password?: string;
  expires_at?: string;
}

export interface UpdateUserPayload {
  first_name?: string;
  last_name?: string;
  middle_name?: string;
  telegram?: string;
  department?: string;
  position?: string;
  role?: string;
  is_active?: boolean;
  blocked?: boolean;
  expires_at?: string;
  password?: string;
}

export interface DashboardData {
  users_count: number;
  blocked_users_count: number;
  pending_requests_count: number;
  logins_today: number;
  logins_total: number;
}

export interface RegistrationRequestItem {
  id: string;
  first_name: string;
  last_name: string;
  middle_name: string;
  email: string;
  telegram: string;
  department: string;
  position: string;
  reason: string;
  status: string;
  created_at: string;
}

export interface RegistrationRequestsResponse {
  requests: RegistrationRequestItem[];
}

export interface StatsItem {
  id: number;
  user_id: string;
  ip: string;
  country: string;
  region: string;
  city: string;
  language: string;
  user_agent: string;
  browser: string;
  os: string;
  device: string;
  screen_resolution: string;
  login_at: string;
  session_duration_seconds: number | null;
  auth_error: string;
}

export interface StatsResponse {
  items: StatsItem[];
  limit: number;
  offset: number;
}

export interface LogItem {
  id: number;
  user_id: string | null;
  action: string;
  entity_type: string;
  entity_id: string;
  ip: string;
  user_agent: string;
  details: Record<string, unknown>;
  created_at: string;
}

export interface LogsResponse {
  items: LogItem[];
  limit: number;
  offset: number;
}

export interface SettingResponse {
  key: string;
  value: string;
}

export interface Features {
  telegram_bot: boolean;
}
