const CONSENT_STORAGE_KEY = "tg_consent_accepted";

export function hasConsent(documentVersion: string): boolean {
  try {
    return window.localStorage.getItem(CONSENT_STORAGE_KEY) === documentVersion;
  } catch {
    return false;
  }
}

export function markConsent(documentVersion: string): void {
  try {
    window.localStorage.setItem(CONSENT_STORAGE_KEY, documentVersion);
  } catch {
    // storage unavailable (private mode) — server still records the consent
  }
}
