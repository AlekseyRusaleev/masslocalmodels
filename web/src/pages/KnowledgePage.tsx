import { useCallback, useEffect, useState } from "react";
import {
  ApiError,
  addDocument,
  deleteDocument,
  fetchDocuments,
  formatSince,
  searchKnowledge,
  type KnowledgeDocument,
  type KnowledgeHit,
} from "../api/client";
import { IconTrash } from "../components/icons";

/**
 * Знания: документы и поиск по ним.
 *
 * Тексты индексируются на узлах сети (эмбеддинги считает та же очередь
 * заданий, что и чат), поэтому страница показывает, на какой модели считали, и
 * честно сообщает, если её не обслуживает никто.
 */
export function KnowledgePage() {
  const [documents, setDocuments] = useState<KnowledgeDocument[]>([]);
  const [hits, setHits] = useState<KnowledgeHit[]>([]);
  const [query, setQuery] = useState("");
  const [title, setTitle] = useState("");
  const [text, setText] = useState("");
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [searched, setSearched] = useState(false);

  const load = useCallback(async () => {
    try {
      setDocuments(await fetchDocuments());
      setError("");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Не удалось получить документы");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const upload = async () => {
    if (busy) return;
    setBusy("upload");
    setError("");
    setNotice("");
    try {
      const document = await addDocument({
        title: title.trim() || undefined,
        text,
      });
      setNotice(
        `Документ «${document.title}» проиндексирован: ${document.chunk_count} фрагм.`,
      );
      setTitle("");
      setText("");
      await load();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Не удалось проиндексировать документ");
    } finally {
      setBusy("");
    }
  };

  const search = async () => {
    const value = query.trim();
    if (!value || busy) return;
    setBusy("search");
    setError("");
    try {
      setHits(await searchKnowledge(value));
      setSearched(true);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Не удалось выполнить поиск");
    } finally {
      setBusy("");
    }
  };

  const remove = async (document: KnowledgeDocument) => {
    if (busy) return;
    setBusy(document.id);
    try {
      await deleteDocument(document.id);
      setNotice(`Документ «${document.title}» удалён`);
      await load();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Не удалось удалить документ");
    } finally {
      setBusy("");
    }
  };

  return (
    <>
      <header className="page-head">
        <div>
          <h1 className="page-title">Знания</h1>
          <p className="page-subtitle">
            Документы, по которым ищет платформа. Векторы считают узлы сети — на той модели
            эмбеддингов, которую они объявили.
          </p>
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
          <h2 className="panel-title">Поиск</h2>
        </div>
        <div style={{ padding: "var(--space-5)" }}>
          <div className="field">
            <label htmlFor="knowledge-query">Запрос</label>
            <input
              id="knowledge-query"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="например: что делать, если задание зависло"
              onKeyDown={(event) => {
                if (event.key === "Enter") {
                  event.preventDefault();
                  void search();
                }
              }}
            />
            <span className="field-note">
              Ищет по смыслу, а не по словам: фрагменты ранжируются по близости векторов.
            </span>
          </div>
          <div className="form-actions">
            <button
              className="button"
              data-variant="primary"
              disabled={busy === "search" || !query.trim()}
              onClick={() => void search()}
            >
              {busy === "search" ? "Ищу…" : "Найти"}
            </button>
          </div>

          {searched && hits.length === 0 && (
            <p className="field-note" style={{ marginTop: "var(--space-4)" }}>
              Ничего не нашлось. Проверьте, что документы загружены, а узел объявил модель эмбеддингов.
            </p>
          )}

          {hits.length > 0 && (
            <div className="kv" style={{ marginTop: "var(--space-4)" }}>
              {hits.map((hit) => (
                <div key={`${hit.document_id}-${hit.ordinal}`} style={{ paddingBottom: "var(--space-3)" }}>
                  <div className="num muted" style={{ fontSize: 12 }}>
                    {hit.document_title} · фрагмент {hit.ordinal + 1} · близость {hit.similarity.toFixed(3)}
                  </div>
                  <div>{hit.content}</div>
                </div>
              ))}
            </div>
          )}
        </div>
      </section>

      <section className="panel">
        <div className="panel-head">
          <h2 className="panel-title">Документы</h2>
          <span className="muted num">{documents.length}</span>
        </div>

        {documents.length === 0 ? (
          <div className="notice">
            <h3 className="notice-title">База знаний пуста</h3>
            <p className="notice-text">
              Вставьте текст ниже — платформа нарежет его на фрагменты, посчитает векторы на узле сети
              и сохранит. Документы видны только вашему аккаунту.
            </p>
          </div>
        ) : (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>Название</th>
                  <th>Фрагментов</th>
                  <th>Загружен</th>
                  <th>Действия</th>
                </tr>
              </thead>
              <tbody>
                {documents.map((document) => (
                  <tr key={document.id}>
                    <td>{document.title}</td>
                    <td className="num">{document.chunk_count}</td>
                    <td className="num secondary">{formatSince(document.created_at)}</td>
                    <td>
                      <button
                        className="button"
                        data-variant="quiet"
                        disabled={busy === document.id}
                        onClick={() => void remove(document)}
                        title="удалить документ и его фрагменты"
                      >
                        <IconTrash />
                        Удалить
                      </button>
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
          <h2 className="panel-title">Добавить документ</h2>
        </div>
        <div style={{ padding: "var(--space-5)" }}>
          <div className="field">
            <label htmlFor="knowledge-title">Название</label>
            <input
              id="knowledge-title"
              value={title}
              onChange={(event) => setTitle(event.target.value)}
              placeholder="пусто — возьмём первую строку текста"
            />
          </div>
          <div className="field">
            <label htmlFor="knowledge-text">Текст</label>
            <textarea
              id="knowledge-text"
              className="composer-input"
              rows={8}
              value={text}
              onChange={(event) => setText(event.target.value)}
              placeholder="Вставьте регламент, заметки или документацию"
            />
            <span className="field-note">
              Текст режется на фрагменты по абзацам с перекрытием, чтобы факты на границе не терялись.
              Индексация занимает несколько секунд на фрагмент.
            </span>
          </div>
          <div className="form-actions">
            <button
              className="button"
              data-variant="primary"
              disabled={busy === "upload" || !text.trim()}
              onClick={() => void upload()}
            >
              {busy === "upload" ? "Индексирую…" : "Проиндексировать"}
            </button>
          </div>
        </div>
      </section>
    </>
  );
}
