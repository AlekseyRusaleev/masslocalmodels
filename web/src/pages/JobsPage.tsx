import { useCallback, useEffect, useState } from "react";
import {
  ApiError,
  fetchJobs,
  formatDuration,
  formatSpeed,
  formatTokens,
  type JobRow,
} from "../api/client";
import { IconRefresh } from "../components/icons";

const stateLabels: Record<string, string> = {
  queued: "В очереди",
  claimed: "У узла",
  streaming: "Генерация",
  done: "Готово",
  failed: "Ошибка",
  cancelled: "Отменено",
};

function speedOf(job: JobRow): number | undefined {
  if (!job.eval_ms || !job.completion_tokens) return undefined;
  return (job.completion_tokens * 1000) / job.eval_ms;
}

/** Человеческая подпись вердикта аттестации: «не проверено» слишком общее. */
function attestationLabel(verdict?: string): string {
  switch (verdict) {
    case "no_manifest":
      return "нет манифеста";
    case "unsigned_manifest":
      return "манифест не подписан";
    case "mismatch":
      return "не сходятся";
    case "not_reported":
      return "узел не сообщил";
    default:
      return "не сверялись";
  }
}

function attestationHint(verdict?: string): string {
  switch (verdict) {
    case "no_manifest":
      return "в каталоге нет ревизии этой модели — сверять не с чем";
    case "unsigned_manifest":
      return "ревизия есть, но её манифест не закреплён подписью платформы";
    case "mismatch":
      return "хэши весов не совпали с манифестом";
    case "not_reported":
      return "узел не сообщил digest весов";
    default:
      return "сверка не выполнялась";
  }
}

export function JobsPage() {
  const [jobs, setJobs] = useState<JobRow[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [auto, setAuto] = useState(true);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      setJobs(await fetchJobs());
      setError(null);
    } catch (err) {
      const apiError = err as ApiError;
      setError(apiError.message ?? "Не удалось получить задания");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (!auto) return;
    const timer = window.setInterval(() => void load(), 5000);
    return () => window.clearInterval(timer);
  }, [auto, load]);

  const done = jobs.filter((job) => job.state === "done");
  const totalTokens = done.reduce((sum, job) => sum + job.completion_tokens, 0);
  const rated = done.filter((job) => speedOf(job) !== undefined);
  const averageSpeed =
    rated.length > 0
      ? rated.reduce((sum, job) => sum + (speedOf(job) ?? 0), 0) / rated.length
      : undefined;

  return (
    <>
      <header className="page-head">
        <div>
          <h1 className="page-title">Задания</h1>
          <p className="page-subtitle">
            Журнал запросов к моделям. Скорость считается по времени генерации на узле — без загрузки модели и
            без сети.
          </p>
        </div>
        <div style={{ display: "flex", gap: "var(--space-2)", alignItems: "center" }}>
          <label className="toggle">
            <input type="checkbox" checked={auto} onChange={(event) => setAuto(event.target.checked)} />
            <span>Автообновление</span>
          </label>
          <button className="button" onClick={() => void load()} disabled={loading}>
            <IconRefresh />
            Обновить
          </button>
        </div>
      </header>

      <section className="stat-row">
        <div className="stat">
          <span className="stat-label">Заданий в журнале</span>
          <span className="stat-value">{jobs.length}</span>
          <span className="stat-note">последние 50</span>
        </div>
        <div className="stat">
          <span className="stat-label">Выполнено</span>
          <span className="stat-value">{done.length}</span>
          <span className="stat-note">за всё время наблюдения</span>
        </div>
        <div className="stat">
          <span className="stat-label">Токенов ответа</span>
          <span className="stat-value">{formatTokens(totalTokens)}</span>
          <span className="stat-note">сумма по завершённым</span>
        </div>
        <div className="stat">
          <span className="stat-label">Средняя скорость</span>
          <span className="stat-value">{formatSpeed(averageSpeed)}</span>
          <span className="stat-note">по {rated.length} замерам</span>
        </div>
      </section>

      <section className="panel">
        <div className="panel-head">
          <h2 className="panel-title">Последние задания</h2>
          <span className="muted num">{jobs.length}</span>
        </div>

        {error && (
          <div className="notice notice-error">
            <h3 className="notice-title">Не удалось получить задания</h3>
            <p className="notice-text">{error}</p>
          </div>
        )}

        {!error && jobs.length === 0 && (
          <div className="notice">
            <h3 className="notice-title">Заданий пока нет</h3>
            <p className="notice-text">Отправьте запрос из раздела «Чат» — он появится в журнале.</p>
          </div>
        )}

        {jobs.length > 0 && (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>Время</th>
                  <th>Модель</th>
                  <th>Состояние</th>
                  <th>Вход</th>
                  <th>Ответ</th>
                  <th>Скорость</th>
                  <th>Генерация</th>
                  <th>Модель узла</th>
                  <th>Веса</th>
                  <th>Причина остановки</th>
                </tr>
              </thead>
              <tbody>
                {jobs.map((job) => (
                  <tr key={job.id}>
                    <td className="secondary num">{new Date(job.created_at).toLocaleTimeString("ru-RU")}</td>
                    <td className="num">{job.model || "—"}</td>
                    <td>
                      <span className="state" data-state={job.state === "done" ? "available" : job.state === "failed" ? "quarantined" : "paused"}>
                        <span className="state-dot" />
                        {stateLabels[job.state] ?? job.state}
                        {(job.attempts ?? 0) > 1 && (
                          <span className="muted"> · попытка {job.attempts}</span>
                        )}
                      </span>
                    </td>
                    <td className="num">{job.prompt_tokens ? formatTokens(job.prompt_tokens) : "—"}</td>
                    <td className="num">{job.completion_tokens ? formatTokens(job.completion_tokens) : "—"}</td>
                    <td className="num">{formatSpeed(speedOf(job))}</td>
                    <td className="num secondary">{formatDuration(job.eval_ms)}</td>
                    <td className="secondary">
                      {job.digest_verified ? (
                        <span className="state" data-state="available" title="digest совпал с подписанным манифестом">
                          <span className="state-dot" />
                          подтверждены
                        </span>
                      ) : (
                        <span
                          className="muted"
                          title={attestationHint(job.attestation)}
                        >
                          {attestationLabel(job.attestation)}
                        </span>
                      )}
                    </td>
                    <td className="secondary num">
                      {job.served_model ? (
                        <>
                          <div>{job.served_model}</div>
                          <div className="muted" style={{ fontSize: 11 }}>
                            {job.served_digest ? `${job.served_digest.slice(0, 12)}…` : "digest не передан"}
                          </div>
                        </>
                      ) : (
                        <span className="muted">не сообщено</span>
                      )}
                    </td>
                    <td className="secondary">
                      {job.error ? <span className="error-text">{job.error}</span> : job.finish_reason || "—"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </>
  );
}
