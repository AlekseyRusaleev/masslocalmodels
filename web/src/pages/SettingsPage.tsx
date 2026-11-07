import { useCallback, useEffect, useState } from "react";
import {
  ApiError,
  changePassword,
  createAccount,
  deleteAccount,
  fetchAccounts,
  fetchMe,
  fetchSessions,
  logout,
  revokeSession,
  setAccountPassword,
} from "../api/client";
import type { Account, SessionInfo, WhoAmI } from "../api/client";
import { formatSince } from "../api/client";

interface Props {
  onSignedOut: () => void;
}

export function SettingsPage({ onSignedOut }: Props) {
  const [me, setMe] = useState<WhoAmI | null>(null);
  const [sessions, setSessions] = useState<SessionInfo[]>([]);
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [passwordBusy, setPasswordBusy] = useState(false);

  const [newEmail, setNewEmail] = useState("");
  const [newName, setNewName] = useState("");
  const [newRole, setNewRole] = useState<"user" | "admin">("user");
  const [newAccountPassword, setNewAccountPassword] = useState("");
  const [accountBusy, setAccountBusy] = useState(false);
  const [issuedFor, setIssuedFor] = useState("");

  const isAdmin = me?.role === "admin";

  const load = useCallback(async () => {
    try {
      const who = await fetchMe();
      setMe(who);
      const list = await fetchSessions();
      setSessions(list);
      if (who.role === "admin") {
        setAccounts(await fetchAccounts());
      }
      setError("");
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        onSignedOut();
        return;
      }
      setError(err instanceof ApiError ? err.message : "Не удалось загрузить настройки");
    }
  }, [onSignedOut]);

  useEffect(() => {
    void load();
  }, [load]);

  const submitPassword = async () => {
    if (passwordBusy) return;
    setPasswordBusy(true);
    setError("");
    setNotice("");
    try {
      await changePassword(currentPassword, newPassword);
      setCurrentPassword("");
      setNewPassword("");
      setNotice("Пароль изменён. Остальные входы закрыты.");
      await load();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Не удалось сменить пароль");
    } finally {
      setPasswordBusy(false);
    }
  };

  const submitAccount = async () => {
    if (accountBusy) return;
    setAccountBusy(true);
    setError("");
    setNotice("");
    try {
      const account = await createAccount({
        email: newEmail.trim(),
        name: newName.trim() || undefined,
        role: newRole,
        password: newAccountPassword || undefined,
      });
      setIssuedFor(`${account.email}${newAccountPassword ? " · пароль задан" : " · пароль не задан"}`);
      setNewEmail("");
      setNewName("");
      setNewAccountPassword("");
      setNewRole("user");
      await load();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Не удалось создать аккаунт");
    } finally {
      setAccountBusy(false);
    }
  };

  const resetPassword = async (account: Account) => {
    const password = window.prompt(`Новый пароль для ${account.email} (не меньше 10 символов):`);
    if (!password) return;
    try {
      await setAccountPassword(account.id, password);
      setNotice(`Пароль обновлён: ${account.email}`);
      await load();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Не удалось задать пароль");
    }
  };

  /** Закрывает доступ аккаунту: сессии и ключи отзываются, вход перестаёт работать. */
  const closeAccess = async (account: Account) => {
    const confirmed = window.confirm(`Закрыть доступ для ${account.email}? Сессии и ключи будут отозваны.`);
    if (!confirmed) return;
    try {
      await deleteAccount(account.id);
      setNotice(`Доступ закрыт: ${account.email}`);
      await load();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Не удалось закрыть доступ");
    }
  };

  const endSession = async (session: SessionInfo) => {
    try {
      await revokeSession(session.id);
      if (session.current) {
        onSignedOut();
        return;
      }
      await load();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Не удалось закрыть сессию");
    }
  };

  return (
    <>
      <header className="page-head">
        <div>
          <h1 className="page-title">Настройки</h1>
          <p className="page-subtitle">Аккаунт, входы и доступ к панели.</p>
        </div>
      </header>

      {error && <div className="notice notice-error">{error}</div>}
      {notice && (
        <div className="notice">
          <p className="notice-text">{notice}</p>
        </div>
      )}

      <section className="panel">
        <div className="panel-head">
          <h2 className="panel-title">Аккаунт</h2>
        </div>
        <div style={{ padding: "var(--space-5)" }}>
          {me ? (
            <div className="kv">
              <div>
                <span className="muted">Почта</span>
                <div className="num">{me.email}</div>
              </div>
              <div>
                <span className="muted">Имя</span>
                <div>{me.name || "—"}</div>
              </div>
              <div>
                <span className="muted">Роль</span>
                <div>{me.role === "admin" ? "администратор" : "участник"}</div>
              </div>
              <div>
                <span className="muted">Права</span>
                <div className="num" style={{ fontSize: 12 }}>
                  {me.scopes.join(", ")}
                </div>
              </div>
              <div>
                <span className="muted">Вход действует до</span>
                <div className="num">
                  {me.expires_at ? new Date(me.expires_at).toLocaleString("ru-RU") : "—"}
                </div>
              </div>
            </div>
          ) : (
            <p className="muted">Загрузка…</p>
          )}
          <div className="form-actions">
            <button
              className="button"
              data-variant="quiet"
              onClick={() => {
                void logout().finally(onSignedOut);
              }}
            >
              Выйти
            </button>
          </div>
        </div>
      </section>

      <section className="panel">
        <div className="panel-head">
          <h2 className="panel-title">Пароль</h2>
        </div>
        <div style={{ padding: "var(--space-5)" }}>
          <div className="field">
            <label htmlFor="current-password">Текущий пароль</label>
            <input
              id="current-password"
              type="password"
              autoComplete="current-password"
              value={currentPassword}
              onChange={(event) => setCurrentPassword(event.target.value)}
            />
          </div>
          <div className="field">
            <label htmlFor="new-password">Новый пароль</label>
            <input
              id="new-password"
              type="password"
              autoComplete="new-password"
              value={newPassword}
              onChange={(event) => setNewPassword(event.target.value)}
            />
            <span className="field-note">
              Не меньше 10 символов. После смены все входы, включая этот, закрываются: панель выдаёт
              новую сессию сразу.
            </span>
          </div>
          <div className="form-actions">
            <button
              className="button"
              data-variant="primary"
              disabled={passwordBusy || newPassword.length < 10}
              onClick={() => void submitPassword()}
            >
              {passwordBusy ? "Сохранение…" : "Сменить пароль"}
            </button>
          </div>
        </div>
      </section>

      <section className="panel">
        <div className="panel-head">
          <h2 className="panel-title">Активные входы</h2>
          <span className="muted" style={{ fontSize: 12 }}>
            {sessions.filter((session) => !session.revoked_at).length}
          </span>
        </div>
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>Клиент</th>
                <th>Начат</th>
                <th>Последний запрос</th>
                <th>Действует до</th>
                <th>Состояние</th>
                <th>Действия</th>
              </tr>
            </thead>
            <tbody>
              {sessions.map((session) => {
                const ended = Boolean(session.revoked_at) || new Date(session.expires_at) < new Date();
                return (
                  <tr key={session.id}>
                    <td>
                      {session.name}
                      {session.current && <span className="muted"> · это устройство</span>}
                    </td>
                    <td className="num secondary">{formatSince(session.created_at)}</td>
                    <td className="num secondary">{formatSince(session.last_used_at)}</td>
                    <td className="num secondary">{new Date(session.expires_at).toLocaleString("ru-RU")}</td>
                    <td>
                      <span className="state" data-state={ended ? "offline" : "available"}>
                        <span className="state-dot" />
                        {ended ? "закрыт" : "активен"}
                      </span>
                    </td>
                    <td>
                      {!ended && (
                        <button className="button" data-variant="quiet" onClick={() => void endSession(session)}>
                          Закрыть
                        </button>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </section>

      {isAdmin && (
        <section className="panel">
          <div className="panel-head">
            <h2 className="panel-title">Доступ к панели</h2>
            <span className="muted" style={{ fontSize: 12 }}>
              {accounts.length}
            </span>
          </div>
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>Почта</th>
                  <th>Имя</th>
                  <th>Роль</th>
                  <th>Пароль</th>
                  <th>Создан</th>
                  <th>Действия</th>
                </tr>
              </thead>
              <tbody>
                {accounts.map((account) => (
                  <tr key={account.id}>
                    <td className="num">{account.email}</td>
                    <td>{account.name || "—"}</td>
                    <td>{account.role === "admin" ? "администратор" : "участник"}</td>
                    <td>
                      <span className="state" data-state={account.has_password ? "available" : "paused"}>
                        <span className="state-dot" />
                        {account.has_password ? "задан" : "не задан"}
                      </span>
                    </td>
                    <td className="num secondary">{formatSince(account.created_at)}</td>
                    <td>
                      <div className="row-actions">
                        <button className="button" data-variant="quiet" onClick={() => void resetPassword(account)}>
                          Задать пароль
                        </button>
                        <button
                          className="button"
                          data-variant="quiet"
                          onClick={() => void closeAccess(account)}
                          title="закрыть доступ и отозвать сессии"
                        >
                          Закрыть доступ
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <div style={{ padding: "var(--space-5)" }}>
            <h3 className="panel-title" style={{ marginBottom: "var(--space-3)" }}>
              Новый аккаунт
            </h3>
            <div className="field">
              <label htmlFor="account-email">Почта</label>
              <input
                id="account-email"
                type="email"
                autoComplete="off"
                spellCheck={false}
                value={newEmail}
                onChange={(event) => setNewEmail(event.target.value)}
              />
            </div>
            <div className="field">
              <label htmlFor="account-name">Имя</label>
              <input
                id="account-name"
                type="text"
                autoComplete="off"
                value={newName}
                onChange={(event) => setNewName(event.target.value)}
              />
            </div>
            <div className="field">
              <label htmlFor="account-role">Роль</label>
              <select
                id="account-role"
                value={newRole}
                onChange={(event) => setNewRole(event.target.value === "admin" ? "admin" : "user")}
              >
                <option value="user">участник</option>
                <option value="admin">администратор</option>
              </select>
            </div>
            <div className="field">
              <label htmlFor="account-password">Пароль</label>
              <input
                id="account-password"
                type="password"
                autoComplete="new-password"
                value={newAccountPassword}
                onChange={(event) => setNewAccountPassword(event.target.value)}
              />
              <span className="field-note">
                Можно оставить пустым и задать позже кнопкой в таблице. Пароль видит только человек, которому
                его передали.
              </span>
            </div>
            <div className="form-actions">
              <button
                className="button"
                data-variant="primary"
                disabled={accountBusy || !newEmail.trim()}
                onClick={() => void submitAccount()}
              >
                {accountBusy ? "Создание…" : "Создать"}
              </button>
            </div>
            {issuedFor && (
              <p className="field-note" style={{ marginTop: "var(--space-4)" }}>
                Готово: <span className="num">{issuedFor}</span>
              </p>
            )}
          </div>
        </section>
      )}

      <section className="panel">
        <div className="panel-head">
          <h2 className="panel-title">Программы и ключи</h2>
        </div>
        <div className="notice">
          <p className="notice-text">
            Приглашение для своей машины выпускается в разделе «Мой узел». Программа скачивается в разделе
            «Программа». Метрики сети отдаются администратору по адресу
            <span className="num"> /metrics</span> в формате Prometheus.
          </p>
        </div>
      </section>
    </>
  );
}
