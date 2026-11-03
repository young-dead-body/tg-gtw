interface LandingProps {
  onLogin: () => void;
  onRegister: () => void;
  loginEnabled: boolean;
}

export function Landing({ onLogin, onRegister, loginEnabled }: LandingProps) {
  return (
    <main className="flex min-h-screen flex-col items-center justify-center bg-slate-50 px-4">
      <div className="w-full max-w-xl text-center">
        <h1 className="text-3xl font-semibold text-slate-900">
          Корпоративный портал доступа
        </h1>
        <p className="mt-4 leading-relaxed text-slate-600">
          Доступ предоставляется авторизованным сотрудникам компании после принятия
          обязательных согласий. Все действия аудируются.
        </p>

        {!loginEnabled && (
          <p className="mt-6 text-sm text-slate-500">
            Примите согласие на обработку персональных данных, чтобы получить доступ
            к входу.
          </p>
        )}
        {loginEnabled && (
          <div className="mt-8 flex flex-col justify-center gap-3 sm:flex-row">
            <button
              type="button"
              onClick={onLogin}
              className="rounded-md bg-blue-600 px-6 py-2.5 text-sm font-medium text-white transition-colors hover:bg-blue-700"
            >
              Войти
            </button>
            <button
              type="button"
              onClick={onRegister}
              className="rounded-md border border-slate-300 bg-white px-6 py-2.5 text-sm font-medium text-slate-700 transition-colors hover:bg-slate-100"
            >
              Регистрация
            </button>
          </div>
        )}

      </div>
    </main>
  );
}
