import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  ApiError,
  appendMessages,
  createConversation,
  deleteConversation,
  fetchConversation,
  fetchConversations,
  fetchModels,
  formatDuration,
  formatSince,
  formatSpeed,
  formatTokens,
  sealMessage,
  streamChat,
  type ChatMessage,
  type ChatUsage,
  type Conversation,
  type ConversationMessage,
  type ServingModel,
} from "../api/client";
import { IconSend, IconStop, IconTrash } from "../components/icons";
import {
  acknowledgeHistoryKey,
  ensureHistoryKey,
  importHistoryPhrase,
  openPair,
  openText,
  sealPair,
  sealText,
  type HistoryKeyStatus,
} from "../historyKey";

const MAX_TOKENS_DEFAULT = 700;

interface Turn extends ChatMessage {
  id: string;
  usage?: ChatUsage;
  elapsedMs?: number;
  error?: string;
  streaming?: boolean;
}

/** Заголовок диалога по первому запросу: сервер его не придумывает. */
function titleFrom(text: string): string {
  const line = text.replace(/\s+/g, " ").trim();
  return line.length > 60 ? `${line.slice(0, 60)}…` : line || "Диалог";
}

async function revealMessage(message: ConversationMessage): Promise<Turn> {
  let content = message.content;
  let reasoning = message.reasoning ?? "";
  if (message.ciphertext && message.nonce && message.key_id) {
    const opened = await openPair({
      key_id: message.key_id,
      nonce: message.nonce,
      ciphertext: message.ciphertext,
    });
    content = opened.content;
    reasoning = opened.reasoning;
  }
  return {
    id: message.id,
    role: message.role === "assistant" ? "assistant" : "user",
    content,
    reasoning,
    usage:
      message.completion_tokens || message.prompt_tokens || message.eval_ms
        ? {
            prompt_tokens: message.prompt_tokens,
            completion_tokens: message.completion_tokens,
            eval_ms: message.eval_ms,
          }
        : undefined,
  };
}

export function ChatPage() {
  const [models, setModels] = useState<ServingModel[]>([]);
  const [model, setModel] = useState("");
  const [turns, setTurns] = useState<Turn[]>([]);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const [keyStatus, setKeyStatus] = useState<HistoryKeyStatus>(() => ensureHistoryKey());
  const [phraseDraft, setPhraseDraft] = useState("");

  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [currentId, setCurrentId] = useState("");
  const [historyLoading, setHistoryLoading] = useState(true);

  const abortRef = useRef<AbortController | null>(null);
  const scrollRef = useRef<HTMLDivElement | null>(null);

  const loadModels = useCallback(async () => {
    try {
      const list = await fetchModels();
      setModels(list);
      setModel((current) => {
        if (current && list.some((m) => m.id === current)) return current;
        return list[0]?.id ?? "";
      });
    } catch (err) {
      const apiError = err as ApiError;
      setNotice(apiError.message ?? "Не удалось получить список моделей");
    }
  }, []);

  const loadConversations = useCallback(async () => {
    try {
      const list = await fetchConversations();
      const named = await Promise.all(
        list.map(async (item) => {
          if (!item.title_key_id || !item.title_nonce || !item.title) return item;
          try {
            const title = await openText({
              key_id: item.title_key_id,
              nonce: item.title_nonce,
              ciphertext: item.title,
            });
            return { ...item, title };
          } catch {
            return { ...item, title: "диалог закрыт другим ключом" };
          }
        }),
      );
      setConversations(named);
      return named;
    } catch {
      // История — удобство, а не условие работы чата: без неё разговор идёт
      // дальше, просто не сохраняется.
      return [] as Conversation[];
    }
  }, []);

  /** Открывает диалог с сервера и восстанавливает ходы. */
  const openConversation = useCallback(async (id: string) => {
    if (!id) {
      setCurrentId("");
      setTurns([]);
      return;
    }
    try {
      const { conversation, messages } = await fetchConversation(id);
      setCurrentId(conversation.id);
      if (conversation.model) setModel(conversation.model);
      const turns = await Promise.all(messages.map((message) => revealMessage(message)));
      setTurns(turns);
      for (const message of messages) {
        if (message.ciphertext || !message.content) continue;
        try {
          const sealed = await sealPair(message.content, message.reasoning ?? "");
          await sealMessage(id, message.id, sealed);
        } catch {
          // Старый открытый текст останется, пока ключ не сможет его закрыть.
        }
      }
      setNotice(null);
    } catch (err) {
      setNotice(err instanceof ApiError ? err.message : "Не удалось открыть диалог");
    }
  }, []);

  useEffect(() => {
    void loadModels();
    const timer = window.setInterval(() => void loadModels(), 20000);
    return () => window.clearInterval(timer);
  }, [loadModels]);

  // При открытии страницы показываем последний диалог: человек возвращается к
  // тому, что писал в прошлый раз, а не к пустому экрану.
  useEffect(() => {
    let cancelled = false;
    void loadConversations().then((list) => {
      if (cancelled) return;
      const first = list[0];
      if (first) {
        void openConversation(first.id);
      }
      setHistoryLoading(false);
    });
    return () => {
      cancelled = true;
    };
  }, [loadConversations, openConversation]);

  useEffect(() => {
    const node = scrollRef.current;
    if (node) node.scrollTop = node.scrollHeight;
  }, [turns]);

  const active = useMemo(() => models.find((m) => m.id === model), [models, model]);

  const startNewChat = () => {
    if (busy) return;
    setCurrentId("");
    setTurns([]);
    setNotice(null);
  };

  const removeCurrent = async (id: string) => {
    if (!id || busy) return;
    try {
      await deleteConversation(id);
      if (id === currentId) {
        setCurrentId("");
        setTurns([]);
      }
      await loadConversations();
    } catch (err) {
      setNotice(err instanceof ApiError ? err.message : "Не удалось удалить диалог");
    }
  };

  const send = async () => {
    const text = draft.trim();
    if (!text || !model || busy) return;

    const userTurn: Turn = { id: `u-${Date.now()}`, role: "user", content: text };
    const answerTurn: Turn = {
      id: `a-${Date.now()}`,
      role: "assistant",
      content: "",
      reasoning: "",
      streaming: true,
    };
    const history = [...turns, userTurn];

    setTurns([...history, answerTurn]);
    setDraft("");
    setBusy(true);
    setNotice(null);

    const controller = new AbortController();
    abortRef.current = controller;
    const startedAt = performance.now();
    let answer = "";
    let reasoning = "";
    let usage: ChatUsage | undefined;
    let failure = "";

    const patch = (change: Partial<Turn>) => {
      setTurns((current) =>
        current.map((turn) => (turn.id === answerTurn.id ? { ...turn, ...change } : turn)),
      );
    };

    try {
      await streamChat({
        model,
        messages: history.map(({ role, content }) => ({ role, content })),
        maxTokens: MAX_TOKENS_DEFAULT,
        signal: controller.signal,
        onDelta: (content, reasonChunk) => {
          answer += content;
          reasoning += reasonChunk;
          patch({ content: answer, reasoning });
        },
        onUsage: (received) => {
          usage = received;
          patch({ usage: received });
        },
      });
      patch({
        streaming: false,
        elapsedMs: performance.now() - startedAt,
        content: answer,
        reasoning,
      });
    } catch (err) {
      if ((err as Error)?.name === "AbortError") {
        patch({ streaming: false, content: answer, reasoning });
      } else {
        const apiError = err as ApiError;
        failure = apiError.message ?? "Запрос не выполнен";
        patch({
          streaming: false,
          content: answer,
          reasoning,
          error: failure,
        });
      }
    } finally {
      setBusy(false);
      abortRef.current = null;
    }

    // Сохраняем ход на сервере: и вопрос, и ответ (даже частичный после
    // остановки). Если сохранить не удалось — говорим об этом прямо: ответ уже
    // показан и не пропадает, но в историю он не попадёт.
    try {
      const userSeal = await sealPair(text, "");
      const assistantSeal = await sealPair(answer || failure || "(ответ не получен)", reasoning);
      const titleSeal = await sealText(titleFrom(text));
      let conversationId = currentId;
      if (!conversationId) {
        const created = await createConversation(titleSeal.ciphertext, model, {
          title_nonce: titleSeal.nonce,
          title_key_id: titleSeal.key_id,
        });
        conversationId = created.id;
        setCurrentId(created.id);
      }
      await appendMessages(conversationId, [
        { role: "user", key_id: userSeal.key_id, nonce: userSeal.nonce, ciphertext: userSeal.ciphertext },
        {
          role: "assistant",
          key_id: assistantSeal.key_id,
          nonce: assistantSeal.nonce,
          ciphertext: assistantSeal.ciphertext,
          prompt_tokens: usage?.prompt_tokens,
          completion_tokens: usage?.completion_tokens,
          eval_ms: usage?.eval_ms,
        },
      ]);
      await loadConversations();
    } catch (err) {
      setNotice(
        err instanceof ApiError
          ? `Диалог не сохранён: ${err.message}`
          : "Диалог не сохранён: история недоступна",
      );
    }
  };

  const stop = () => {
    abortRef.current?.abort();
  };

  return (
    <>
      <header className="page-head">
        <div>
          <h1 className="page-title">Чат</h1>
          <p className="page-subtitle">
            Приватный запрос считает только ваша машина. История на сервере хранится шифротекстом: ключ остаётся в
            этом браузере.
          </p>
        </div>
        <div className="chat-toolbar">
          <label className="select-field">
            <span>Модель</span>
            <select
              value={model}
              onChange={(event) => setModel(event.target.value)}
              disabled={busy || models.length === 0}
            >
              {models.length === 0 && <option value="">нет доступных</option>}
              {models.map((item) => (
                <option key={item.id} value={item.id}>
                  {item.id}
                </option>
              ))}
            </select>
          </label>
        </div>
      </header>

      {models.length === 0 && (
        <div className="notice">
          <h3 className="notice-title">Нет модели, которую можно вызвать</h3>
          <p className="notice-text">
            Ни один узел не сообщил, какую модель он обслуживает. Запустите на машине участника
            <span className="num"> compute-node run --model qwen3:8b</span>: узел пришлёт heartbeat, и
            модель появится здесь.
          </p>
        </div>
      )}

      {keyStatus.needsAck && (
        <div className="notice">
          <h3 className="notice-title">Ключ истории на этом компьютере</h3>
          <p className="notice-text">
            Запишите фразу. Без неё другой браузер не откроет переписку. На сервер она не отправляется.
          </p>
          <p className="num">{keyStatus.phrase}</p>
          <button
            className="button"
            data-variant="primary"
            type="button"
            onClick={() => {
              acknowledgeHistoryKey();
              setKeyStatus(ensureHistoryKey());
            }}
          >
            Я записал фразу
          </button>
        </div>
      )}

      <form
        className="connect-form"
        onSubmit={(event) => {
          event.preventDefault();
          try {
            importHistoryPhrase(phraseDraft);
            setPhraseDraft("");
            setKeyStatus(ensureHistoryKey());
            setNotice("Ключ восстановлен. Откройте диалог ещё раз.");
          } catch (err) {
            setNotice(err instanceof Error ? err.message : "Фраза не подошла");
          }
        }}
      >
        <label className="field-inline">
          <span>Фраза восстановления</span>
          <input value={phraseDraft} onChange={(event) => setPhraseDraft(event.target.value)} />
        </label>
        <button className="button" type="submit">
          Восстановить ключ
        </button>
      </form>
      {notice && (
        <div className="notice notice-error">
          <h3 className="notice-title">Замечание</h3>
          <p className="notice-text">{notice}</p>
        </div>
      )}

      <section className="panel chat-panel">
        <div className="chat-split">
          <aside className="chat-history" aria-label="Диалоги">
            <div className="chat-history-head">
              <span className="panel-title" style={{ fontSize: 13 }}>
                Диалоги
              </span>
              <button
                type="button"
                className="button"
                data-variant="quiet"
                onClick={startNewChat}
                disabled={busy}
              >
                Новый
              </button>
            </div>

            {historyLoading && <p className="history-meta">загрузка…</p>}
            {!historyLoading && conversations.length === 0 && (
              <p className="history-meta">Разговоров ещё нет: первый появится после запроса.</p>
            )}

            <ul className="history-list">
              {conversations.map((conversation) => (
                <li
                  key={conversation.id}
                  className="history-item"
                  data-active={conversation.id === currentId}
                >
                  <button
                    type="button"
                    className="history-open"
                    onClick={() => void openConversation(conversation.id)}
                    disabled={busy}
                    title={conversation.title || "без названия"}
                  >
                    <span className="history-title">{conversation.title || "без названия"}</span>
                    <span className="history-meta num">
                      {conversation.message_count} сообщ. · {formatSince(conversation.updated_at)}
                    </span>
                  </button>
                  <button
                    type="button"
                    className="history-delete"
                    title="удалить диалог"
                    onClick={() => void removeCurrent(conversation.id)}
                    disabled={busy}
                  >
                    <IconTrash />
                  </button>
                </li>
              ))}
            </ul>
          </aside>

          <div className="chat-main">
            <div className="chat-log" ref={scrollRef}>
              {turns.length === 0 && (
                <div className="chat-empty">
                  <p className="chat-empty-title">Диалог пуст</p>
                  <p className="chat-empty-text">
                    Напишите запрос. Модель считает на узле участника, платформа только передаёт токены и
                    считает скорость. Ход сохранится в истории под вашим аккаунтом.
                  </p>
                </div>
              )}

          {turns.map((turn) => (
            <article key={turn.id} className="turn" data-role={turn.role}>
              <div className="turn-role">{turn.role === "user" ? "Вы" : "Модель"}</div>

              {turn.role === "assistant" && turn.reasoning ? (
                <details className="reasoning">
                  <summary>Размышления модели · {turn.reasoning.length.toLocaleString("ru-RU")} симв.</summary>
                  <div className="reasoning-body">{turn.reasoning}</div>
                </details>
              ) : null}

              <div className="turn-body">
                {turn.content || (turn.streaming ? <span className="caret" aria-label="генерация" /> : "—")}
                {turn.streaming && turn.content ? <span className="caret caret-inline" /> : null}
              </div>

              {turn.error && <div className="turn-error">{turn.error}</div>}

              {turn.role === "assistant" && !turn.streaming && (turn.usage || turn.elapsedMs) && (
                <div className="turn-meta num">
                  {turn.usage?.completion_tokens
                    ? `${formatTokens(turn.usage.completion_tokens)} токенов`
                    : "токены неизвестны"}
                  {turn.usage?.tokens_per_second ? ` · ${formatSpeed(turn.usage.tokens_per_second)}` : ""}
                  {turn.usage?.eval_ms ? ` · генерация ${formatDuration(turn.usage.eval_ms)}` : ""}
                  {turn.elapsedMs ? ` · весь запрос ${formatDuration(turn.elapsedMs)}` : ""}
                </div>
              )}
            </article>
          ))}
        </div>

          <form
            className="composer"
            onSubmit={(event) => {
              event.preventDefault();
              void send();
            }}
          >
            <textarea
              className="composer-input"
              value={draft}
              placeholder={model ? `Запрос к ${model}` : "Сначала появится доступная модель"}
              rows={3}
              disabled={busy || !model}
              onChange={(event) => setDraft(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
                  event.preventDefault();
                  void send();
                }
              }}
            />
            <div className="composer-actions">
              <span className="composer-hint">
                {active ? `машин с моделью: ${active.served_by.length}` : "модель молчит"}
                {" · Ctrl+Enter — отправить · "}
                до {MAX_TOKENS_DEFAULT} токенов ответа
              </span>
              {busy ? (
                <button type="button" className="button" onClick={stop}>
                  <IconStop />
                  Остановить
                </button>
              ) : (
                <button type="submit" className="button" data-variant="primary" disabled={!draft.trim() || !model}>
                  <IconSend />
                  Отправить
                </button>
              )}
            </div>
          </form>
          </div>
        </div>
      </section>
    </>
  );
}
