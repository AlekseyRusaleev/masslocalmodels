import { Fragment, useCallback, useEffect, useState } from "react";
import { ApiError, fetchTopologies, setTopologyStatus } from "../api/client";
import type { TopologyDetail, TopologyRow } from "../api/client";

const statusLabels: Record<string, string> = {
  draft: "Черновик",
  staging: "Подготовка",
  active: "Активна",
  draining: "Выводится",
  retired: "Выведена",
  failed: "Ошибка",
};

/**
 * Сколько слоёв покрывают этапы.
 *
 * Число берётся из самих этапов: пустая топология показывается как «этапы не
 * записаны», а не как «0 слоёв» — это разные вещи, и вторая выглядела бы
 * ошибкой данных.
 */
function layerSpan(detail: TopologyDetail): string {
  const stages = detail.stages ?? [];
  if (stages.length === 0) {
    return detail.topology.total_layers > 0 ? `всего ${detail.topology.total_layers}` : "не размещено";
  }
  const starts = stages.map((stage) => stage.layer_start);
  const ends = stages.map((stage) => stage.layer_end);
  const covered = Math.max(...ends) - Math.min(...starts) + 1;
  const total = detail.topology.total_layers > 0 ? ` из ${detail.topology.total_layers}` : "";
  return `${covered}${total}`;
}

/** Короткий идентификатор: в таблице хватает восьми символов. */
function shortId(value?: string): string {
  if (!value) return "—";
  return `${value.slice(0, 8)}…`;
}

export function TopologyPage() {
  const [rows, setRows] = useState<TopologyDetail[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const [openId, setOpenId] = useState("");
  const [loaded, setLoaded] = useState(false);

  const load = useCallback(async () => {
    try {
      const data = await fetchTopologies();
      setRows(data);
      setError("");
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        setError("Нужен вход администратора.");
        return;
      }
      setError(err instanceof ApiError ? err.message : "Не удалось загрузить топологии");
    } finally {
      setLoaded(true);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const change = async (id: string, status: string) => {
    setBusy(id);
    try {
      await setTopologyStatus(id, status);
      await load();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Не удалось изменить состояние");
    } finally {
      setBusy("");
    }
  };

  const topologyOf = (detail: TopologyDetail): TopologyRow => detail.topology;

  return (
    <>
      <header className="page-head">
        <div>
          <h1 className="page-title">Топология</h1>
          <p className="page-subtitle">
            Размещение модели по слоям между узлами: кто координатор и какой участок модели на ком.
          </p>
        </div>
      </header>

      {error && <div className="notice notice-error">{error}</div>}

      {loaded && rows.length === 0 && !error && (
        <section className="panel">
          <div className="panel-head">
            <h2 className="panel-title">Размещений пока нет</h2>
          </div>
          <div style={{ padding: "var(--space-5)" }}>
            <p className="field-note">
              Топология нужна, когда модель не влезает в одну карту и делится по слоям. Сейчас каждый узел
              обслуживает модель целиком, поэтому список пуст — это состояние сети, а не ошибка загрузки.
            </p>
          </div>
        </section>
      )}

      {rows.length > 0 && (
        <section className="panel">
          <div className="panel-head">
            <h2 className="panel-title">Версии размещения</h2>
            <span className="muted" style={{ fontSize: 12 }}>
              {rows.length}
            </span>
          </div>
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>Модель</th>
                  <th>Версия</th>
                  <th>Координатор</th>
                  <th>Режим</th>
                  <th>Слоёв покрыто</th>
                  <th>Состояние</th>
                  <th>Этапы</th>
                  <th>Действия</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((detail) => {
                  const topology = topologyOf(detail);
                  const expanded = openId === topology.id;
                  return (
                    <Fragment key={topology.id}>
                      <tr>
                        <td>
                          <div>{topology.model_name || shortId(topology.revision_id)}</div>
                          <div className="muted" style={{ fontSize: 12 }}>
                            {shortId(topology.revision_id)}
                          </div>
                        </td>
                        <td className="num">{topology.version}</td>
                        <td className="num">{shortId(topology.coordinator_worker_id)}</td>
                        <td className="num">{topology.split_mode || "—"}</td>
                        <td className="num">{layerSpan(detail)}</td>
                        <td>
                          <span
                            className="state"
                            data-state={topology.status === "active" ? "available" : "paused"}
                          >
                            <span className="state-dot" />
                            {statusLabels[topology.status] ?? topology.status}
                          </span>
                          {/* Почему группа не поднялась: числа, а не «ошибка карты». */}
                          {topology.error && (
                            <div className="muted" style={{ fontSize: 12, marginTop: 4 }}>
                              {topology.error}
                            </div>
                          )}
                        </td>
                        <td className="num">
                          <button
                            className="button"
                            data-variant="quiet"
                            onClick={() => setOpenId(expanded ? "" : topology.id)}
                          >
                            {expanded ? "свернуть" : `показать (${detail.stages?.length ?? 0})`}
                          </button>
                        </td>
                        <td>
                          <div className="row-actions">
                            {topology.status !== "active" && (
                              <button
                                className="button"
                                data-variant="quiet"
                                disabled={busy === topology.id}
                                onClick={() => void change(topology.id, "active")}
                              >
                                Активировать
                              </button>
                            )}
                            {topology.status === "active" && (
                              <button
                                className="button"
                                data-variant="quiet"
                                disabled={busy === topology.id}
                                onClick={() => void change(topology.id, "draining")}
                              >
                                Выводить
                              </button>
                            )}
                          </div>
                        </td>
                      </tr>
                      {expanded && (
                        <tr>
                          <td colSpan={8}>
                            {(detail.stages ?? []).length === 0 && (
                              <span className="muted">
                                Этапы не записаны: у этой версии нет разбиения по узлам.
                              </span>
                            )}
                            {(detail.stages ?? []).map((stage) => (
                              <div
                                key={stage.id}
                                style={{ display: "flex", gap: "var(--space-4)", padding: "2px 0" }}
                              >
                                <span className="num">
                                  слои {stage.layer_start}–{stage.layer_end}
                                </span>
                                <span>{stage.worker_name || shortId(stage.worker_id)}</span>
                                <span className="muted num">{(stage.share * 100).toFixed(0)} %</span>
                                <span className="muted">{stage.role}</span>
                              </div>
                            ))}
                          </td>
                        </tr>
                      )}
                    </Fragment>
                  );
                })}
              </tbody>
            </table>
          </div>
        </section>
      )}

      <section className="panel">
        <div className="panel-head">
          <h2 className="panel-title">Чего здесь ещё нет</h2>
        </div>
        <div style={{ padding: "var(--space-5)" }}>
          <p className="field-note">
            Автоматического планировщика размещения нет: топологию создаёт администратор через API. Узел,
            которому не хватает VRAM, пока не предлагает разделение сам.
          </p>
        </div>
      </section>
    </>
  );
}
