import { useCallback, useEffect, useState } from "react";
import {
  ApiError,
  fetchOwnNodes,
  formatGB,
  formatSince,
  formatSpeed,
  setOwnNodeState,
  type WorkerRow,
} from "../api/client";
import { IconRefresh } from "../components/icons";
import { ConnectNodePanel } from "../components/ConnectNodePanel";

export function MinePage() {
  const [nodes, setNodes] = useState<WorkerRow[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      setNodes(await fetchOwnNodes());
      setError("");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Не удалось получить машины");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const toggle = async (node: WorkerRow) => {
    const next = node.state === "paused" ? "available" : "paused";
    try {
      await setOwnNodeState(node.id, next);
      await load();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Не удалось сменить состояние");
    }
  };

  return (
    <>
      <header className="page-head">
        <div>
          <h1 className="page-title">Мой узел</h1>
          <p className="page-subtitle">
            Приватные чаты считает только машина этого аккаунта. Если памяти одной карты не хватает,
            подключите ещё свои компьютеры — чужой узел этот текст не получит.
          </p>
        </div>
        <button className="button" onClick={() => void load()} disabled={loading}>
          <IconRefresh />
          Обновить
        </button>
      </header>

      {error && <div className="notice notice-error">{error}</div>}

      <section className="panel">
        <div className="panel-head">
          <h2 className="panel-title">Мои компьютеры</h2>
          <span className="muted num">{nodes.length}</span>
        </div>
        {nodes.length === 0 && !loading && (
          <div className="notice">
            <h3 className="notice-title">Своих машин ещё нет</h3>
            <p className="notice-text">
              Выпустите приглашение ниже и вставьте его в compute-node.exe. Пока машины нет, приватный чат ждать некого.
            </p>
          </div>
        )}
        {nodes.length > 0 && (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>Узел</th>
                  <th>Состояние</th>
                  <th>Модель</th>
                  <th>VRAM свободно</th>
                  <th>Скорость</th>
                  <th>Heartbeat</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {nodes.map((node) => (
                  <tr key={node.id}>
                    <td>{node.name}</td>
                    <td>{node.state}</td>
                    <td className="num">{node.served_model || "—"}</td>
                    <td className="num">{formatGB(node.vram_free_bytes)}</td>
                    <td className="num">{node.last_tokens_per_second ? formatSpeed(node.last_tokens_per_second) : "—"}</td>
                    <td className="secondary">{formatSince(node.last_seen_at)}</td>
                    <td>
                      <button className="button" type="button" onClick={() => void toggle(node)}>
                        {node.state === "paused" ? "Возобновить" : "Пауза"}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <ConnectNodePanel onEnrolled={() => void load()} />
    </>
  );
}
