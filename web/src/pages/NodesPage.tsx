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
  isConnected,
  participantCounts,
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

  const { total: participants, online: participantsOnline } = participantCounts(workers);
  const connected = workers.filter((worker) => isConnected(worker));
  const stale = workers.filter((worker) => !isConnected(worker));

  return (
    <>
      <header className="page-head">
        <div>
          <h1 className="page-title">Участники</h1>
          <p className="page-subtitle">
            Участник — человек с зарегистрированным компьютером. В сети он считается, пока его программа присылает
            heartbeat: окно те же две минуты, за которые платформа собирает группу слоёв. Старые регистрации в эти
            числа не входят.
          </p>
        </div>
        <button className="button" onClick={() => void load()} disabled={status === "loading"}>
          <IconRefresh />
          Обновить
        </button>
      </header>

      <section className="stat-row">
        <div className="stat">
          <span className="stat-label">Участников всего</span>
          <span className="stat-value">{participants}</span>
          <span className="stat-note">разные аккаунты, когда-либо подключавшие компьютер</span>
        </div>
        <div className="stat">
          <span className="stat-label">В сети сейчас</span>
          <span className="stat-value">{participantsOnline}</span>
          <span className="stat-note">heartbeat за последние 2 минуты</span>
        </div>
      </section>

      <section className="panel">
        <div className="panel-head">
          <h2 className="panel-title">Сейчас в сети</h2>
          <span className="muted num">{connected.length}</span>
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

        {status !== "error" && connected.length === 0 && (
          <div className="notice">
            <h3 className="notice-title">Сейчас никто не в сети</h3>
            <p className="notice-text">
              Компьютер попадает сюда, когда его программа запущена и за последние две минуты прислала heartbeat.
            </p>
          </div>
        )}

        {connected.length > 0 && (
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
                {connected.map((worker) => {
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

      {stale.length > 0 && (
        <details className="panel">
          <summary className="panel-head">
            <h2 className="panel-title">Раньше регистрировались, сейчас не в сети</h2>
            <span className="muted num">{stale.length}</span>
          </summary>
          <p className="notice-text">
            Это прошлые запуски программы. Они не подключены, и их видеокарты в сеть не входят.
          </p>
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>Компьютер</th>
                  <th>Последний heartbeat</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {stale.map((worker) => {
                  const busy = pending === worker.id;
                  return (
                    <tr key={worker.id}>
                      <td>
                        <div>{worker.name}</div>
                        <div className="muted" style={{ fontSize: 12 }}>
                          {worker.hostname}
                        </div>
                      </td>
                      <td className="secondary">{formatSince(worker.last_heartbeat_at ?? worker.last_seen_at)}</td>
                      <td>
                        {confirmDelete === worker.id ? (
                          <div className="row-actions">
                            <button className="button" disabled={busy} onClick={() => void remove(worker)}>
                              Удалить
                            </button>
                            <button className="button" data-variant="quiet" onClick={() => setConfirmDelete(null)}>
                              Отмена
                            </button>
                          </div>
                        ) : (
                          <button
                            className="button"
                            data-variant="quiet"
                            title="Удалить регистрацию"
                            disabled={busy}
                            onClick={() => setConfirmDelete(worker.id)}
                          >
                            <IconTrash />
                          </button>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </details>
      )}

      <ConnectNodePanel onEnrolled={() => void load()} />
    </>
  );
}
