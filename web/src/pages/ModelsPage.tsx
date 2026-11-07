import { useCallback, useEffect, useState } from "react";
import {
  ApiError,
  addArtifacts,
  createRevision,
  fetchCatalog,
  fetchModels,
  fetchWorkers,
  formatGB,
  formatSince,
  setRevisionStatus,
  signRevision,
  type ArtifactInput,
  type ModelRevision,
  type ServingModel,
  type WorkerRow,
} from "../api/client";
import { IconRefresh } from "../components/icons";

const revisionStatusLabels: Record<string, string> = {
  draft: "Черновик",
  ready: "Готова",
  active: "Активна",
  retired: "Выведена",
};

/**
 * Разбор строки «url [sha256] [слои N-M]».
 *
 * Человеку проще вставить адреса файлов и хеши списком, чем заполнять форму на
 * каждый файл. Хеш проверяется по длине, диапазон слоёв — по виду «12-27».
 */
function parseArtifactLine(line: string, index: number): ArtifactInput | string {
  const parts = line.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) return `строка ${index + 1}: пусто`;
  const [url, sha = "", layers = ""] = parts;
  if (!/^https?:\/\//.test(url)) return `строка ${index + 1}: адрес должен начинаться с http(s)://`;
  if (sha && !/^[0-9a-f]{64}$/i.test(sha)) return `строка ${index + 1}: sha256 — 64 шестнадцатеричных символа`;
  const artifact: ArtifactInput = { name: url.split("/").pop() || `file-${index + 1}`, url, sha256: sha.toLowerCase() };
  if (layers) {
    const match = /^(\d+)-(\d+)$/.exec(layers);
    if (!match) return `строка ${index + 1}: слои задаются как 12-27`;
    artifact.layer_start = Number(match[1]);
    artifact.layer_end = Number(match[2]);
  }
  return artifact;
}

export function ModelsPage({ admin = false }: { admin?: boolean }) {
  const [serving, setServing] = useState<ServingModel[]>([]);
  const [workers, setWorkers] = useState<WorkerRow[]>([]);
  const [catalog, setCatalog] = useState<ModelRevision[]>([]);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState("");

  const [form, setForm] = useState({
    name: "",
    display_name: "",
    source: "",
    revision: "",
    license: "",
    license_checked: false,
    quantization: "",
    params_billions: "",
  });
  const [artifactTarget, setArtifactTarget] = useState("");
  const [artifactLines, setArtifactLines] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const models = await fetchModels();
      setServing(models);
      if (admin) {
        setWorkers(await fetchWorkers());
      } else {
        setWorkers([]);
      }
      setError("");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Не удалось получить каталог");
    } finally {
      setLoading(false);
    }
    if (!admin) {
      setCatalog([]);
      return;
    }
    try {
      setCatalog(await fetchCatalog());
    } catch {
      setCatalog([]);
    }
  }, [admin]);

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), 15000);
    return () => window.clearInterval(timer);
  }, [load]);

  const registerRevision = async () => {
    if (busy) return;
    setBusy("new");
    setError("");
    setNotice("");
    try {
      const created = await createRevision({
        name: form.name.trim(),
        display_name: form.display_name.trim() || undefined,
        source: form.source.trim() || undefined,
        revision: form.revision.trim() || undefined,
        license: form.license.trim() || undefined,
        license_checked: form.license_checked,
        quantization: form.quantization.trim() || undefined,
        params_billions: form.params_billions ? Number(form.params_billions) : undefined,
      });
      setNotice(`Ревизия создана: ${created.revision.model_name} (${created.revision.id.slice(0, 8)}…). Файлы не добавлены.`);
      setForm({ name: "", display_name: "", source: "", revision: "", license: "", license_checked: false, quantization: "", params_billions: "" });
      await load();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Не удалось создать ревизию");
    } finally {
      setBusy("");
    }
  };

  const uploadArtifacts = async () => {
    if (!artifactTarget) {
      setError("Выберите ревизию, к которой относятся файлы.");
      return;
    }
    const lines = artifactLines.split("\n").filter((line) => line.trim());
    if (lines.length === 0) {
      setError("Добавьте хотя бы один файл.");
      return;
    }
    const artifacts: ArtifactInput[] = [];
    for (const [index, line] of lines.entries()) {
      const parsed = parseArtifactLine(line, index);
      if (typeof parsed === "string") {
        setError(parsed);
        return;
      }
      artifacts.push(parsed);
    }
    setBusy("artifacts");
    setError("");
    try {
      await addArtifacts(artifactTarget, artifacts);
      setNotice(`Файлов добавлено: ${artifacts.length}`);
      setArtifactLines("");
      await load();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Не удалось добавить файлы");
    } finally {
      setBusy("");
    }
  };

  /** Подписывает манифест: после правки файлов подпись нужно ставить заново. */
  const sign = async (id: string) => {
    setBusy(id);
    setError("");
    try {
      await signRevision(id);
      setNotice("Манифест подписан: аттестация весов будет подтверждать эту ревизию.");
      await load();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Не удалось подписать манифест");
    } finally {
      setBusy("");
    }
  };

  const changeStatus = async (id: string, status: string) => {
    setBusy(id);
    setError("");
    try {
      await setRevisionStatus(id, status);
      await load();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Не удалось изменить состояние");
    } finally {
      setBusy("");
    }
  };

  const servingNodes = new Set(serving.flatMap((model) => model.served_by));
  const idleNodes = workers.filter((worker) => !servingNodes.has(worker.name));

  return (
    <>
      <header className="page-head">
        <div>
          <h1 className="page-title">Модели</h1>
          <p className="page-subtitle">
            Что сеть обслуживает прямо сейчас — по heartbeat узлов, и каталог ревизий для раздачи весов.
          </p>
        </div>
        <button className="button" onClick={() => void load()} disabled={loading}>
          <IconRefresh />
          Обновить
        </button>
      </header>

      {error && <div className="notice notice-error">{error}</div>}
      {notice && (
        <div className="notice">
          <p className="notice-text">{notice}</p>
        </div>
      )}

      <section className="panel">
        <div className="panel-head">
          <h2 className="panel-title">Обслуживаются сейчас</h2>
          <span className="muted num">{serving.length}</span>
        </div>

        {serving.length === 0 && !loading && (
          <div className="notice">
            <h3 className="notice-title">Ни одна модель не обслуживается</h3>
            <p className="notice-text">
              Узел присылает имя модели в каждом heartbeat. Если список пуст, значит агент запущен без
              модели: <span className="num">compute-node run --model qwen3:8b</span>.
            </p>
          </div>
        )}

        {serving.length > 0 && (
          <div className="card-grid">
            {serving.map((model) => (
              <article key={model.id} className="model-card">
                <h3 className="model-name num">{model.id}</h3>
                <dl className="kv">
                  <dt>Узлы</dt>
                  <dd>{admin ? model.served_by.join(", ") || "—" : String(model.served_by.length)}</dd>
                  <dt>Последний heartbeat</dt>
                  <dd className="num">{formatSince(model.last_seen)}</dd>
                  <dt>Запрос</dt>
                  <dd className="num">POST /v1/chat/completions</dd>
                </dl>
              </article>
            ))}
          </div>
        )}
      </section>

      {admin && (
      <>
      <section className="panel">
        <div className="panel-head">
          <h2 className="panel-title">Каталог ревизий</h2>
          <span className="muted num">{catalog.length}</span>
        </div>

        {catalog.length === 0 ? (
          <div className="notice">
            <h3 className="notice-title">Каталог пуст</h3>
            <p className="notice-text">
              Ревизии нужны для раздачи весов и разбиения модели по узлам. Пока узел обслуживает модель
              целиком, каталог может оставаться пустым — это не ошибка.
            </p>
          </div>
        ) : (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>Модель</th>
                  <th>Ревизия</th>
                  <th>Квантование</th>
                  <th>Параметров</th>
                  <th>Файлов</th>
                  <th>Лицензия</th>
                  <th>Манифест</th>
                  <th>Состояние</th>
                  <th>Действия</th>
                </tr>
              </thead>
              <tbody>
                {catalog.map((row) => (
                  <tr key={row.id}>
                    <td>
                      <div className="num">{row.model_name}</div>
                      <div className="muted" style={{ fontSize: 12 }}>
                        {row.runtime} · {formatGB(row.size_bytes)}
                      </div>
                    </td>
                    <td className="num secondary">
                      {row.revision ? `${row.revision.slice(0, 10)}…` : "—"}
                      <div className="muted" style={{ fontSize: 12 }}>
                        {row.source || "источник не указан"}
                      </div>
                    </td>
                    <td className="secondary">{row.quantization || "—"}</td>
                    <td className="num">{row.params_billions ? `${row.params_billions} B` : "—"}</td>
                    <td className="num">
                      {row.artifact_count}
                      {row.artifact_count === 0 && <div className="muted" style={{ fontSize: 12 }}>не готово к раздаче</div>}
                    </td>
                    <td className="secondary">
                      {row.license || "—"}
                      <div className="muted" style={{ fontSize: 12 }}>
                        {row.license_checked ? "проверена" : "не проверена"}
                      </div>
                    </td>
                    <td className="secondary">
                      {row.manifest_signature ? (
                        <span className="state" data-state="available" title={row.manifest_signature}>
                          <span className="state-dot" />
                          подписан
                        </span>
                      ) : (
                        <span className="muted">не подписан</span>
                      )}
                    </td>
                    <td>
                      <span
                        className="state"
                        data-state={row.status === "active" ? "available" : row.status === "retired" ? "offline" : "paused"}
                      >
                        <span className="state-dot" />
                        {revisionStatusLabels[row.status] ?? row.status}
                      </span>
                    </td>
                    <td>
                      <div className="row-actions">
                        {row.status !== "active" && (
                          <button
                            className="button"
                            data-variant="quiet"
                            disabled={busy === row.id || row.artifact_count === 0}
                            title={row.artifact_count === 0 ? "сначала добавьте файлы" : undefined}
                            onClick={() => void changeStatus(row.id, "active")}
                          >
                            Активировать
                          </button>
                        )}
                        {row.status === "active" && (
                          <button
                            className="button"
                            data-variant="quiet"
                            disabled={busy === row.id}
                            onClick={() => void changeStatus(row.id, "retired")}
                          >
                            Вывести
                          </button>
                        )}
                        <button
                          className="button"
                          data-variant="quiet"
                          onClick={() => setArtifactTarget(row.id)}
                        >
                          Файлы
                        </button>
                        <button
                          className="button"
                          data-variant="quiet"
                          disabled={busy === row.id || row.artifact_count === 0}
                          title={row.artifact_count === 0 ? "сначала добавьте файлы" : "закрепить хэши подписью"}
                          onClick={() => void sign(row.id)}
                        >
                          Подписать
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section className="panel">
        <div className="panel-head">
          <h2 className="panel-title">Новая ревизия</h2>
        </div>
        <div style={{ padding: "var(--space-5)" }}>
          <div className="field">
            <label htmlFor="revision-name">Имя модели</label>
            <input
              id="revision-name"
              value={form.name}
              spellCheck={false}
              onChange={(event) => setForm({ ...form, name: event.target.value })}
              placeholder="qwen3-8b"
            />
            <span className="field-note">Имя, по которому модель ищут узлы и клиенты.</span>
          </div>
          <div className="field">
            <label htmlFor="revision-source">Источник</label>
            <input
              id="revision-source"
              value={form.source}
              spellCheck={false}
              onChange={(event) => setForm({ ...form, source: event.target.value })}
              placeholder="huggingface.co/Qwen/Qwen3-8B-GGUF"
            />
          </div>
          <div className="field">
            <label htmlFor="revision-ref">Ревизия источника</label>
            <input
              id="revision-ref"
              value={form.revision}
              spellCheck={false}
              onChange={(event) => setForm({ ...form, revision: event.target.value })}
              placeholder="commit hash, не ветка"
            />
            <span className="field-note">
              Закреплённый commit: плавающая ветка означала бы, что завтра узлы скачают другие веса.
            </span>
          </div>
          <div className="field">
            <label htmlFor="revision-license">Лицензия</label>
            <input
              id="revision-license"
              value={form.license}
              spellCheck={false}
              onChange={(event) => setForm({ ...form, license: event.target.value })}
              placeholder="Apache-2.0"
            />
          </div>
          <div className="field">
            <label htmlFor="revision-quant">Квантование</label>
            <input
              id="revision-quant"
              value={form.quantization}
              spellCheck={false}
              onChange={(event) => setForm({ ...form, quantization: event.target.value })}
              placeholder="Q4_K_M"
            />
          </div>
          <div className="field">
            <label htmlFor="revision-params">Параметров, млрд</label>
            <input
              id="revision-params"
              value={form.params_billions}
              inputMode="decimal"
              onChange={(event) => setForm({ ...form, params_billions: event.target.value })}
              placeholder="8"
            />
            <span className="field-note">Нужно прогнозу скорости: по размеру модели считается время отклика.</span>
          </div>
          <div className="field field-inline">
            <input
              id="revision-license-ok"
              type="checkbox"
              checked={form.license_checked}
              onChange={(event) => setForm({ ...form, license_checked: event.target.checked })}
            />
            <label htmlFor="revision-license-ok">Условия лицензии проверены</label>
          </div>
          <div className="form-actions">
            <button
              className="button"
              data-variant="primary"
              disabled={busy === "new" || !form.name.trim()}
              onClick={() => void registerRevision()}
            >
              {busy === "new" ? "Создание…" : "Создать ревизию"}
            </button>
          </div>
        </div>
      </section>

      <section className="panel">
        <div className="panel-head">
          <h2 className="panel-title">Файлы ревизии</h2>
        </div>
        <div style={{ padding: "var(--space-5)" }}>
          <div className="field">
            <label htmlFor="artifact-target">Ревизия</label>
            <select
              id="artifact-target"
              value={artifactTarget}
              onChange={(event) => setArtifactTarget(event.target.value)}
            >
              <option value="">— выберите —</option>
              {catalog.map((row) => (
                <option key={row.id} value={row.id}>
                  {row.model_name} · {row.id.slice(0, 8)}
                </option>
              ))}
            </select>
          </div>
          <div className="field">
            <label htmlFor="artifact-lines">Файлы: по одному в строке</label>
            <textarea
              id="artifact-lines"
              className="composer-input"
              rows={5}
              value={artifactLines}
              spellCheck={false}
              onChange={(event) => setArtifactLines(event.target.value)}
              placeholder={"https://…/qwen3-8b-Q4_K_M-00001-of-00002.gguf <sha256> 0-17\nhttps://…/qwen3-8b-Q4_K_M-00002-of-00002.gguf <sha256> 18-35"}
            />
            <span className="field-note">
              Формат: адрес, sha256 (64 символа), необязательный диапазон слоёв «N-M». Узел скачивает файлы
              напрямую и сверяет хеш; без файлов ревизию нельзя активировать.
            </span>
          </div>
          <div className="form-actions">
            <button
              className="button"
              data-variant="primary"
              disabled={busy === "artifacts" || !artifactTarget || !artifactLines.trim()}
              onClick={() => void uploadArtifacts()}
            >
              {busy === "artifacts" ? "Добавление…" : "Добавить файлы"}
            </button>
          </div>
        </div>
      </section>

      <section className="panel">
        <div className="panel-head">
          <h2 className="panel-title">Узлы без модели</h2>
          <span className="muted num">{idleNodes.length}</span>
        </div>

        {idleNodes.length === 0 ? (
          <div className="notice">
            <h3 className="notice-title">Все узлы заявили модель</h3>
            <p className="notice-text">Каждый подключённый узел сообщает, какую модель он готов выполнять.</p>
          </div>
        ) : (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>Узел</th>
                  <th>Состояние</th>
                  <th>VRAM свободно</th>
                  <th>Heartbeat</th>
                </tr>
              </thead>
              <tbody>
                {idleNodes.map((worker) => (
                  <tr key={worker.id}>
                    <td>
                      <div>{worker.name}</div>
                      <div className="muted" style={{ fontSize: 12 }}>
                        {worker.hostname}
                      </div>
                    </td>
                    <td className="secondary">{worker.state}</td>
                    <td className="num">{formatGB(worker.vram_free_bytes)}</td>
                    <td className="secondary">{formatSince(worker.last_heartbeat_at ?? worker.last_seen_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
      </>
      )}
    </>
  );
}
