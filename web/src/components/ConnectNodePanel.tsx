import { useEffect, useState } from "react";
import {
  ApiError,
  createApiKey,
  createEnrollment,
  encodeInvite,
  fetchHealth,
  fetchModels,
  type EnrollmentToken,
  type Invite,
} from "../api/client";
import { IconRefresh } from "../components/icons";

/**
 * Подключение новой машины одним приглашением.
 *
 * Токен и ключ принадлежат тому, кто нажал кнопку. Оба показываются один раз.
 */
export function ConnectNodePanel({ onEnrolled }: { onEnrolled: () => void }) {
  const [label, setLabel] = useState("");
  const [model, setModel] = useState("");
  const [controlURL, setControlURL] = useState("https://api.nshno.tech");
  const [token, setToken] = useState<EnrollmentToken | null>(null);
  const [apiKey, setApiKey] = useState<string | null>(null);
  const [invite, setInvite] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    void fetchHealth()
      .then((health) => {
        if (health.control_url) setControlURL(health.control_url.replace(/\/$/, ""));
      })
      .catch(() => undefined);
    void fetchModels()
      .then((models) => {
        const best = [...models].sort((a, b) => b.served_by.length - a.served_by.length)[0];
        if (best) setModel(best.id);
      })
      .catch(() => undefined);
  }, []);

  const issue = async () => {
    setBusy(true);
    setError(null);
    setCopied(false);
    const name = label.trim() || "новый узел";
    let created: EnrollmentToken | null = null;
    try {
      created = await createEnrollment(name);
      setToken(created);
      const key = await createApiKey(`харнес · ${name}`);
      const payload: Invite = {
        network_name: "Compute Network",
        control_url: controlURL,
        enrollment_token: created.token,
        api_key: key.token,
      };
      if (model.trim()) payload.model = model.trim();
      setApiKey(key.token);
      setInvite(encodeInvite(payload));
      onEnrolled();
    } catch (err) {
      const apiError = err as ApiError;
      if (created) {
        setError(
          `${apiError.message ?? "Ключ харнеса не выпущен"}. Токен регистрации уже создан и показан ниже — выпустите ключ отдельно или новое приглашение.`,
        );
      } else {
        setError(apiError.message ?? "Не удалось выпустить приглашение");
      }
    } finally {
      setBusy(false);
    }
  };

  const copy = async () => {
    if (!invite) return;
    await navigator.clipboard.writeText(invite);
    setCopied(true);
  };

  const reset = () => {
    setToken(null);
    setApiKey(null);
    setInvite(null);
    setCopied(false);
  };

  return (
    <section className="panel">
      <div className="panel-head">
        <h2 className="panel-title">Подключить компьютер</h2>
        {invite && (
          <button className="button" data-variant="quiet" onClick={reset}>
            Скрыть секреты
          </button>
        )}
      </div>

      <div className="connect-body">
        <p className="notice-text" style={{ margin: 0 }}>
          Приглашение привязывает машину к вашему аккаунту. Токен одноразовый, 24 часа. Секреты
          показываются один раз: в базе лежат только их хеши.
          {model ? ` Модель сети сейчас: ${model}.` : ""}
        </p>

        <div className="connect-form">
          <label className="field-inline">
            <span>Подпись узла</span>
            <input
              value={label}
              placeholder="например partner-3060"
              onChange={(event) => setLabel(event.target.value)}
              disabled={busy}
            />
          </label>
          <button className="button" data-variant="primary" onClick={() => void issue()} disabled={busy}>
            <IconRefresh />
            Выпустить приглашение
          </button>
        </div>

        {error && <p className="error-text">{error}</p>}

        {token && !invite && (
          <p className="num">Токен регистрации: {token.token}</p>
        )}

        {invite && token && apiKey && (
          <>
            <div className="connect-token">
              <span className="muted">Код приглашения</span>
              <code className="num">{invite}</code>
              <button className="button" type="button" onClick={() => void copy()}>
                {copied ? "Скопировано" : "Копировать"}
              </button>
              <span className="muted num">
                токен регистрации до {new Date(token.expires_at).toLocaleString("ru-RU")}
              </span>
            </div>
            <div className="connect-command">
              <span className="muted">Что делать на компьютере</span>
              <pre className="num">{`1. Скачайте compute-node.exe в разделе «Программа» и запустите его.
2. Вставьте код приглашения в окно и нажмите «Подключиться».
   Либо в PowerShell из папки с файлами:
   .\\install-worker-agent.ps1 -Invite "<код>" -Binary .\\compute-node.exe
3. Программа проверит GPU и Ollama, скачает модель и оставит значок в трее.`}</pre>
            </div>
          </>
        )}
      </div>
    </section>
  );
}
