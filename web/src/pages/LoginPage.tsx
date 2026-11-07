import { useState } from "react";
import type { FormEvent } from "react";
import { ApiError, login } from "../api/client";

interface Props {
  onSignedIn: () => void;
}

/** Тексты ошибок входа: сервер отвечает кодами, человеку нужен смысл. */
function loginError(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return "Control Plane не отвечает. Проверьте соединение.";
  }
  switch (error.code) {
    case "invalid_credentials":
      return "Неверная почта или пароль.";
    case "password_not_set":
      return "У аккаунта ещё нет пароля. Попросите администратора задать его.";
    case "too_many_attempts":
      return "Слишком много неудачных попыток. Повторите через несколько минут.";
    case "forbidden":
      return "Аккаунт не активен.";
    default:
      return error.message;
  }
}

export function LoginPage({ onSignedIn }: Props) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      await login(email.trim(), password);
      onSignedIn();
    } catch (err) {
      setError(loginError(err));
      setPassword("");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="login-wrap">
      <form className="login-card" onSubmit={submit}>
        <div className="brand">
          <span className="brand-mark" aria-hidden="true" />
          <span className="brand-name">Compute Network</span>
        </div>
        <p className="page-subtitle">Вход в панель управления сетью.</p>

        <div className="field">
          <label htmlFor="email">Почта</label>
          <input
            id="email"
            type="email"
            autoComplete="username"
            spellCheck={false}
            value={email}
            onChange={(event) => setEmail(event.target.value)}
            required
          />
        </div>

        <div className="field">
          <label htmlFor="password">Пароль</label>
          <input
            id="password"
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            required
          />
        </div>

        {error && (
          <p className="error-text" role="alert">
            {error}
          </p>
        )}

        <button
          className="button"
          data-variant="primary"
          type="submit"
          disabled={busy || !email.trim() || !password}
        >
          {busy ? "Вход…" : "Войти"}
        </button>

        <p className="field-note">
          Сессия живёт семь дней и отзывается в «Настройках». Пароль задаёт администратор командой
          <span className="num"> set-password</span> на сервере.
        </p>
      </form>
    </div>
  );
}
