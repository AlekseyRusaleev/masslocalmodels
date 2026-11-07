import { useCallback, useEffect, useState } from "react";
import {
  ApiError,
  deleteWorker,
  fetchAttestation,
  fetchWorkers,
  formatGB,
  formatSince,
  formatSpeed,
  formatTokens,
  setWorkerState,
  setWorkerTrust,
  stateLabels,
  type ModelAttestation,
  type WorkerRow,
  type WorkerState,
} from "../api/client";
import { IconRefresh, IconTrash } from "../components/icons";
import { ConnectNodePanel } from "../components/ConnectNodePanel";

type Status = "loading" | "ready" | "error";

export function NodesPage() {
  const [workers, setWorkers] = useState<WorkerRow[]>([]);
  const [status, setStatus] = useState<Status>("loading");
  const [error, setError] = useState<{ code: string; message: string } | null>(null);
  const [pending, setPending] = useState<string | null>(null);
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null);
  // Аттестация весов: какие модели узел держит и сходятся ли они с манифестом.
  const [attestation, setAttestation] = useState<Record<string, ModelAttestation[]>>({});

  const load = useCallback(async () => {
    setStatus("loading");
    setError(null);
    try {
      setWorkers(await fetchWorkers());
      // Аттестация — вспомогательные данные: её отсутствие не должно скрывать узлы.
      try {
        const rows = await fetchAttestation();
        const byWorker: Record<string, ModelAttestation[]> = {};
        for (const row of rows) byWorker[row.worker_id] = row.models;
        setAttestation(byWorker);
      } catch {
        setAttestation({});
      }
      setStatus("ready");
    } catch (err) {
      const apiError = err as ApiError;
      setError({
        code: apiError.code ?? "unknown",
        message: apiError.message ?? "Не удалось получить список узлов",
      });
      setStatus("error");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    const timer = window.setInterval(() => void load(), 15000);
    return () => window.clearInterval(timer);
  }, [load]);

  const changeTrust = async (worker: WorkerRow, next: "private" | "trusted" | "public") => {
    setPending(worker.id);
    try {
      await setWorkerTrust(worker.id, next);
      await load();
    } catch (err) {
      setError({ code: "unknown", message: err instanceof ApiError ? err.message : "Не удалось изменить класс доверия" });
    } finally {
      setPending(null);
    }
  };

  const changeState = async (worker: WorkerRow, next: WorkerState) => {
    setPending(worker.id);
    setError(null);
    try {
      await setWorkerState(worker.id, next);
      await load();
    } catch (err) {
      const apiError = err as ApiError;
      setError({ code: apiError.code ?? "unknown", message: apiError.message ?? "Не удалось изменить состояние" });
      setStatus("error");
    } finally {
      setPending(null);
    }
  };

  const remove = async (worker: WorkerRow) => {
    setPending(worker.id);
    setError(null);
    try {
      await deleteWorker(worker.id);
      setConfirmDelete(null);
      await load();
    } catch (err) {
      const apiError = err as ApiError;
      setError({ code: apiError.code ?? "unknown", message: apiError.message ?? "Не удалось удалить узел" });
      setStatus("error");
    } finally {
      setPending(null);
    }
  };

  const totalVRAM = workers.reduce((sum, worker) => sum + worker.gpu_vram_bytes, 0);
  const totalGPUs = workers.reduce((sum, worker) => sum + worker.gpu_count, 0);
  const online = workers.filter((worker) => worker.last_heartbeat_at).length;
  const activeJobs = workers.reduce((sum, worker) => sum + (worker.active_jobs ?? 0), 0);
  const jobsDone = workers.reduce((sum, worker) => sum + worker.jobs_done, 0);

  return (
    <>
      <header className="page-head">
        <div>
          <h1 className="page-title">Узлы</h1>
          <p className="page-subtitle">
            Компьютеры участников и их телеметрия. Состояние меняется здесь и применяется на узле при следующем
            heartbeat.
          </p>
        </div>
        <button className="button" onClick={() => void load()} disabled={status === "loading"}>
          <IconRefresh />
          Обновить
        </button>
      </header>

      <section className="stat-row">
        <div className="stat">
          <span className="stat-label">Узлов всего</span>
          <span className="stat-value">{workers.length}</span>
          <span className="stat-note">{online} с heartbeat</span>
        </div>
        <div className="stat">
          <span className="stat-label">GPU</span>
          <span className="stat-value">{totalGPUs}</span>
          <span className="stat-note">видеокарт в сети</span>
        </div>
        <div className="stat">
          <span className="stat-label">VRAM суммарно</span>
          <span className="stat-value">{formatGB(totalVRAM)}</span>
          <span className="stat-note">по зарегистрированным узлам</span>
        </div>
        <div className="stat">
          <span className="stat-label">Задания сейчас</span>
          <span className="stat-value">{activeJobs}</span>
          <span className="stat-note">всего выполнено: {jobsDone}</span>
        </div>
      </section>

      <section className="panel">
        <div className="panel-head">
          <h2 className="panel-title">Зарегистрированные узлы</h2>
          <span className="muted num">{workers.length}</span>
        </div>

        {status === "error" && (
          <div className="notice notice-error">
            <h3 className="notice-title">Не удалось получить данные</h3>
            <p className="notice-text">
              {error?.message} ({error?.code}). Сессия могла истечь: войдите снова в разделе «Настройки».
              Control Plane должен быть доступен.
            </p>
          </div>
        )}

        {status !== "error" && workers.length === 0 && (
          <div className="notice">
            <h3 className="notice-title">Узлов пока нет</h3>
            <p className="notice-text">
              Подключите компьютер: выпустите enrollment-токен, затем на машине выполните
              <span className="num"> compute-node join</span> и
              <span className="num"> compute-node run --model ...</span>.
            </p>
          </div>
        )}

        {workers.length > 0 && (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>Узел</th>
                  <th>Состояние</th>
                  <th>Модель · окно</th>
                  <th>Веса</th>
                  <th>Доверие</th>
                  <th>GPU · VRAM</th>
                  <th>Свободно</th>
                  <th>Темп.</th>
                  <th>Задания</th>
                  <th>Скорость</th>
                  <th>RTT</th>
                  <th>Heartbeat</th>
                  <th>Действия</th>
                </tr>
              </thead>
              <tbody>
                {workers.map((worker) => {
                  const busy = pending === worker.id;
                  return (
                    <tr key={worker.id}>
                      <td>
                        <div>{worker.name}</div>
                        <div className="muted" style={{ fontSize: 12 }}>
                          {worker.hostname} · agent {worker.agent_version || "—"}
                        </div>
                      </td>
                      <td>
                        <span className="state" data-state={worker.state}>
                          <span className="state-dot" />
                          {stateLabels[worker.state] ?? worker.state}
                        </span>
                      </td>
                      <td className="num">
                        {worker.served_model || <span className="muted">не заявлена</span>}
                        {worker.served_model && (
                          <div className="muted" style={{ fontSize: 12 }}>
                            {worker.num_ctx ? `${formatTokens(worker.num_ctx)} токенов` : "окно не заявлено"}
                          </div>
                        )}
                        {worker.served_embedding_model && (
                          <div className="muted num" style={{ fontSize: 12 }}>
                            эмбеддинги: {worker.served_embedding_model}
                          </div>
                        )}
                      </td>
                      <td className="secondary">
                        {(() => {
                          const models = attestation[worker.id] ?? [];
                          const served = worker.served_model
                            ? models.find((model) => model.model === worker.served_model)
                            : models[0];
                          if (!served) return <span className="muted">нет данных</span>;
                          const selfCheck = served.self_verified ? " · файл весов узел проверил сам" : "";
                          if (served.verified) {
                            return (
                              <span className="state" data-state="available" title={`${served.digest}${selfCheck}`}>
                                <span className="state-dot" />
                                подтверждены
                              </span>
                            );
                          }
                          const label =
                            served.verdict === "unsigned_manifest"
                              ? "манифест не подписан"
                              : served.verdict === "no_manifest"
                                ? "манифест не задан"
                                : "не сходятся";
                          return (
                            <span className="muted" title={`${served.reason || served.digest}${selfCheck}`}>
                              {label}
                            </span>
                          );
                        })()}
                      </td>
                      <td>
                        <select
                          className="num"
                          value={worker.trust_class}
                          disabled={pending === worker.id}
                          onChange={(event) =>
                            void changeTrust(worker, event.target.value as "private" | "trusted" | "public")
                          }
                        >
                          <option value="private">своя</option>
                          <option value="trusted">проверенный</option>
                          <option value="public">публичный</option>
                        </select>
                      </td>
                      <td className="num">
                        {formatGB(worker.gpu_vram_bytes)}
                        <span className="muted"> · {worker.gpu_count} шт.</span>
                      </td>
                      <td className="num">
                        {formatGB(worker.vram_free_bytes)}
                        <div className="muted" style={{ fontSize: 12 }}>
                          диск {formatGB(worker.disk_free_bytes)}
                        </div>
                      </td>
                      <td className="num">{worker.temperature_c ? `${Math.round(worker.temperature_c)} °C` : "—"}</td>
                      <td className="num">
                        {worker.active_jobs ?? 0}
                        <div className="muted" style={{ fontSize: 12 }}>
                          готово {worker.jobs_done}
                          {worker.jobs_failed > 0 ? ` · ошибок ${worker.jobs_failed}` : ""}
                        </div>
                      </td>
                      <td className="num">{formatSpeed(worker.last_tokens_per_second)}</td>
                      <td className="num secondary">{worker.rtt_ms ? `${worker.rtt_ms} мс` : "—"}</td>
                      <td className="secondary">{formatSince(worker.last_heartbeat_at ?? worker.last_seen_at)}</td>
                      <td>
                        <div className="row-actions">
                          <button
                            className="button"
                            data-variant="quiet"
                            disabled={busy || worker.state === "paused"}
                            onClick={() => void changeState(worker, "paused")}
                          >
                            Пауза
                          </button>
                          <button
                            className="button"
                            data-variant="quiet"
                            disabled={busy || worker.state === "available"}
                            onClick={() => void changeState(worker, "available")}
                          >
                            Возобновить
                          </button>
                          {confirmDelete === worker.id ? (
                            <>
                              <button className="button" disabled={busy} onClick={() => void remove(worker)}>
                                Удалить
                              </button>
                              <button className="button" data-variant="quiet" onClick={() => setConfirmDelete(null)}>
                                Отмена
                              </button>
                            </>
                          ) : (
                            <button
                              className="button"
                              data-variant="quiet"
                              title="Удалить узел из каталога"
                              disabled={busy}
                              onClick={() => setConfirmDelete(worker.id)}
                            >
                              <IconTrash />
                            </button>
                          )}
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <ConnectNodePanel onEnrolled={() => void load()} />
    </>
  );
}
