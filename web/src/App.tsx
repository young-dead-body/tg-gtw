import { useEffect, useState } from "react";
import { Account } from "./components/Account";
import { ResetPassword } from "./components/ResetPassword";
import { AdminPanel } from "./components/AdminPanel";
import { ConsentBanner } from "./components/ConsentBanner";
import { Landing } from "./components/Landing";
import { LoginForm } from "./components/LoginForm";
import { RegisterForm } from "./components/RegisterForm";
import { getConsentInfo, getMe } from "./lib/api-calls";
import { hasConsent, markConsent } from "./lib/consent";
import { FeaturesProvider } from "./lib/features";
import { redirectToTg } from "./lib/navigation";
import type { User } from "./lib/types";

type View = "landing" | "login" | "register" | "admin";

export function App() {
  const [resetToken] = useState(() => {
    const token = new URLSearchParams(window.location.hash.slice(1)).get("reset");
    return token;
  });
  useEffect(() => {
    if (resetToken) window.history.replaceState(null, "", window.location.pathname + window.location.search);
  }, [resetToken]);
  if (resetToken) return <ResetPassword token={resetToken} />;
  return (
    <FeaturesProvider>
      <AppContent />
    </FeaturesProvider>
  );
}

function AppContent() {
  const [loading, setLoading] = useState(true);
  const [view, setView] = useState<View>("landing");
  const [account, setAccount] = useState<User | null>(null);
  const [admin, setAdmin] = useState<User | null>(null);
  const [documentVersion, setDocumentVersion] = useState("1.0");
  const [consentAccepted, setConsentAccepted] = useState(false);
  // True when the *authenticated* user has no consent record server-side (the
  // anonymous-consent linking on login is best-effort, see auth.go
  // userConsentRequired). The banner must then be shown regardless of the
  // localStorage flag, and POST /consent records it against the session.
  const [serverConsentRequired, setServerConsentRequired] = useState(false);

  useEffect(() => {
    let cancelled = false;

    const boot = async () => {
      let sessionUser: User | null = null;
      try {
        const me = await getMe();
        if (cancelled) return;
        if (new URLSearchParams(window.location.search).get("account") === "1") {
          setAccount(me.user); setLoading(false); return;
        }
        if (me.user.role === "admin") {
          setAdmin(me.user);
          setView("admin");
          setLoading(false);
          return;
        }
        if (!me.consent_required) {
          redirectToTg();
          return;
        }
        sessionUser = me.user;
      } catch {
        // no active session — stay on the landing page
      }

      try {
        const info = await getConsentInfo();
        if (cancelled) return;
        setDocumentVersion(info.document_version);
        setConsentAccepted(hasConsent(info.document_version));
        if (sessionUser) setServerConsentRequired(true);
      } catch {
        // fall back to the default version if the API is unreachable
      } finally {
        if (!cancelled) setLoading(false);
      }
    };

    boot();
    return () => {
      cancelled = true;
    };
  }, []);

  const handleConsentAccepted = (version: string) => {
    setDocumentVersion(version);
    markConsent(version);
    setConsentAccepted(true);
    if (serverConsentRequired) {
      redirectToTg();
    }
  };

  if (loading) {
    return (
      <main className="flex min-h-screen items-center justify-center bg-slate-50">
        <p className="text-sm text-slate-500">Загрузка...</p>
      </main>
    );
  }

  if (account) return <Account user={account} />;

  if (view === "admin" && admin) {
    return (
      <AdminPanel
        admin={admin}
        onLoggedOut={() => {
          setAdmin(null);
          setView("landing");
        }}
      />
    );
  }

  if (view === "login") {
    return (
      <LoginForm
        onSuccess={(result) => {
          if (result.user.role === "admin") {
            setAdmin(result.user); setView("admin");
          } else if (result.consent_required) {
            // Account has no consent record yet — ask for it now that the
            // user is identified, then continue to /tg/.
            setServerConsentRequired(true);
            setView("landing");
          } else {
            redirectToTg();
          }
        }}
        onBack={() => setView("landing")}
      />
    );
  }

  if (view === "register") {
    return <RegisterForm onBack={() => setView("landing")} />;
  }

  return (
    <>
      <Landing
        onLogin={() => setView("login")}
        onRegister={() => setView("register")}
        loginEnabled={consentAccepted && !serverConsentRequired}
      />
      {(!consentAccepted || serverConsentRequired) && (
        <ConsentBanner documentVersion={documentVersion} onAccepted={handleConsentAccepted} />
      )}
    </>
  );
}
