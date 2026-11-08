/** Клиент Control Plane. Сессия панели — bearer-токен в localStorage (ADR 0012).
 *  Ключ истории чата хранится отдельно и на сервер не отправляется. */

const TOKEN_KEY = "cn.admin_token";

export type WorkerState = "available" | "paused" | "drain" | "maintenance" | "quarantined" | "offline";

/** Окно, в котором платформа ещё считает компьютер участником сети. */
export const heartbeatFreshMs = 2 * 60 * 1000;

export function isConnected(worker: { last_heartbeat_at?: string }, now = Date.now()): boolean {
  if (!worker.last_heartbeat_at) return false;
  const at = Date.parse(worker.last_heartbeat_at);
  return Number.isFinite(at) && now - at <= heartbeatFreshMs;
}

export function participantCounts(workers: Pick<WorkerRow, "id" | "owner_user_id" | "last_heartbeat_at">[]): {
  total: number;
  online: number;
} {
  const owner = (worker: { id: string; owner_user_id?: string }) => worker.owner_user_id || worker.id;
  const onlineOwners = new Set(workers.filter((worker) => isConnected(worker)).map(owner));
  return { total: new Set(workers.map(owner)).size, online: onlineOwners.size };
}

export interface WorkerRow {
  id: string;
  owner_user_id?: string;
  name: string;
  state: WorkerState;
  trust_class: string;
  agent_version: string;
  os: string;
  hostname: string;
  enrolled_at: string;
  last_seen_at?: string;
  served_model?: string;
  /** Окно контекста узла в токенах. 0 — узел не сообщил. */
  num_ctx?: number;
  /** Модель эмбеддингов, которую обслуживает узел. */
  served_embedding_model?: string;
  cpu_cores?: number;
  ram_bytes?: number;
  gpu_count: number;
  gpu_vram_bytes: number;
  disk_free_bytes?: number;
  last_heartbeat_state?: string;
  last_heartbeat_at?: string;
  rtt_ms?: number;
  active_jobs?: number;
  vram_free_bytes?: number;
  temperature_c?: number;
  cpu_percent?: number;
  jobs_done: number;
  jobs_failed: number;
  last_tokens_per_second?: number;
}

/** Модель, которую прямо сейчас обслуживает узел (из данных heartbeat). */
export interface ServingModel {
  id: string;
  object: string;
  owned_by: string;
  served_by: string[];
  last_seen: string;
}

export interface JobRow {
  id: string;
  worker_id?: string;
  model: string;
  kind: string;
  state: string;
  created_at: string;
  claimed_at?: string;
  finished_at?: string;
  error?: string;
  prompt_tokens: number;
  completion_tokens: number;
  eval_ms: number;
  finish_reason?: string;
  /** Очередь и аренда. */
  priority?: number;
  attempts?: number;
  lease_expires_at?: string;
  /** Чем узел реально обслужил задание. */
  served_model?: string;
  served_digest?: string;
  /** digest совпал с подписанным манифестом каталога. */
  digest_verified?: boolean;
  /** Вердикт сверки: verified, no_manifest, unsigned_manifest, mismatch, not_reported. */
  attestation?: string;
}

export class ApiError extends Error {
  status: number;
  code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

export function getToken(): string {
  return localStorage.getItem(TOKEN_KEY) ?? "";
}

export function setToken(token: string): void {
  if (token) {
    localStorage.setItem(TOKEN_KEY, token);
  } else {
    localStorage.removeItem(TOKEN_KEY);
  }
}

export function maskToken(token: string): string {
  if (!token) return "";
  if (token.length <= 14) return "***";
  return `${token.slice(0, 14)}…`;
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const token = getToken();
  const headers = new Headers(init?.headers);
  headers.set("Accept", "application/json");
  if (init?.body) {
    headers.set("Content-Type", "application/json");
  }
  if (token) {
    headers.set("Authorization", `Bearer ${token}`);
  }

  const response = await fetch(`/api${path}`, { ...init, headers });

  if (!response.ok) {
    let code = "http_error";
    let message = `HTTP ${response.status}`;
    try {
      const body = (await response.json()) as { error?: { code?: string; message?: string } };
      if (body.error?.code) code = body.error.code;
      if (body.error?.message) message = body.error.message;
    } catch {
      // тело не JSON — оставляем статус
    }
    if (response.status === 401 && token) {
      setToken("");
      window.dispatchEvent(new Event("cn-unauthorized"));
    }
    throw new ApiError(response.status, code, message);
  }

  if (response.status === 204) {
    return undefined as T;
  }
  return (await response.json()) as T;
}

export interface HealthResponse {
  status: string;
  version: string;
  control_url?: string;
  checks?: Record<string, string>;
}

export async function fetchHealth(): Promise<HealthResponse> {
  return request<HealthResponse>("/health");
}

/** Пользователь панели: кто вошёл и что ему разрешено. */
export interface Account {
  id: string;
  email: string;
  name?: string;
  role: "user" | "admin";
  status: string;
  has_password?: boolean;
  created_at?: string;
}

export interface SessionInfo {
  id: string;
  name: string;
  user_agent?: string;
  created_at: string;
  last_used_at?: string;
  expires_at: string;
  revoked_at?: string;
  current: boolean;
}

export interface WhoAmI {
  id: string;
  email: string;
  name?: string;
  role: string;
  status: string;
  scopes: string[];
  session: string;
  expires_at?: string;
}

/**
 * Вход в панель по email и паролю.
 *
 * Токен сессии хранится в localStorage: панель и API живут на разных путях
 * одного хоста, а cookie потребовали бы CSRF-защиты. Сессию можно отозвать
 * в «Настройках», а срок её жизни ограничен сервером.
 */
export async function login(email: string, password: string): Promise<{ token: string; user: Account }> {
  const body = await request<{ token: string; user: Account; expires_at: string }>("/v1/auth/login", {
    method: "POST",
    body: JSON.stringify({ email, password }),
  });
  setToken(body.token);
  return body;
}

/** Кто я: используется как проверка «сессия ещё жива» при загрузке панели. */
export async function fetchMe(): Promise<WhoAmI> {
  return request<WhoAmI>("/v1/auth/me");
}

/** Выход: сервер отзывает сессию, браузер забывает токен. */
export async function logout(): Promise<void> {
  try {
    await request("/v1/auth/logout", { method: "POST" });
  } finally {
    setToken("");
  }
}

/** Смена своего пароля. Сервер отзывает все сессии и выдаёт новую. */
export async function changePassword(currentPassword: string, newPassword: string): Promise<void> {
  const body = await request<{ token: string }>("/v1/auth/password", {
    method: "POST",
    body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }),
  });
  if (body.token) setToken(body.token);
}

export async function fetchSessions(): Promise<SessionInfo[]> {
  const body = await request<{ data: SessionInfo[] }>("/v1/auth/sessions");
  return body.data ?? [];
}

export async function revokeSession(id: string): Promise<void> {
  await request(`/v1/auth/sessions/${id}`, { method: "DELETE" });
}

export async function fetchAccounts(): Promise<Account[]> {
  const body = await request<{ data: Account[] }>("/admin/v1/users");
  return body.data ?? [];
}

export async function createAccount(input: {
  email: string;
  name?: string;
  role?: "user" | "admin";
  password?: string;
}): Promise<Account> {
  return request<Account>("/admin/v1/users", {
    method: "POST",
    body: JSON.stringify(input),
  });
}

/** Закрывает доступ: аккаунт помечается удалённым, сессии и ключи отзываются. */
export async function deleteAccount(id: string): Promise<void> {
  await request(`/admin/v1/users/${id}`, { method: "DELETE" });
}

export async function setAccountPassword(id: string, password: string): Promise<void> {
  await request(`/admin/v1/users/${id}/password`, {
    method: "POST",
    body: JSON.stringify({ password }),
  });
}

/** Ревизия модели в каталоге: закреплённый источник и лицензия. */
export interface ModelRevision {
  id: string;
  model_id: string;
  model_name: string;
  source: string;
  /** Закреплённая ревизия источника (commit), а не плавающая ветка. */
  revision: string;
  license: string;
  license_checked: boolean;
  runtime: string;
  quantization: string;
  params_billions?: number;
  size_bytes: number;
  status: string;
  created_at: string;
  artifact_count: number;
  /** Пусто — манифест не закреплён подписью платформы. */
  manifest_signature?: string;
  manifest_signed_at?: string;
}

export interface RevisionInput {
  name: string;
  display_name?: string;
  source?: string;
  revision?: string;
  license?: string;
  license_checked?: boolean;
  runtime?: string;
  quantization?: string;
  params_billions?: number;
}

export interface ArtifactInput {
  name: string;
  url: string;
  sha256?: string;
  size_bytes?: number;
  layer_start?: number;
  layer_end?: number;
}

export async function fetchCatalog(): Promise<ModelRevision[]> {
  const body = await request<{ data: ModelRevision[] }>("/admin/v1/models");
  return body.data ?? [];
}

export async function createRevision(input: RevisionInput): Promise<{ revision: ModelRevision }> {
  return request<{ revision: ModelRevision }>("/admin/v1/models", {
    method: "POST",
    body: JSON.stringify(input),
  });
}

/** Закрепляет манифест ревизии подписью платформы: без неё веса не аттестуются. */
export async function signRevision(id: string): Promise<string> {
  const body = await request<{ signature: string }>(`/admin/v1/models/${id}/sign`, { method: "POST" });
  return body.signature;
}

export async function setRevisionStatus(id: string, status: string): Promise<void> {
  await request(`/admin/v1/models/${id}/status`, {
    method: "POST",
    body: JSON.stringify({ status }),
  });
}

export async function addArtifacts(revisionId: string, artifacts: ArtifactInput[]): Promise<void> {
  await request(`/admin/v1/models/${revisionId}/artifacts`, {
    method: "POST",
    body: JSON.stringify({ artifacts }),
  });
}

/** Диалог панели: живёт на сервере, виден только своему владельцу. */
export interface Conversation {
  id: string;
  title: string;
  title_nonce?: string;
  title_key_id?: string;
  model: string;
  created_at: string;
  updated_at: string;
  message_count: number;
}

export interface ConversationMessage {
  id: string;
  conversation_id: string;
  ordinal: number;
  role: "user" | "assistant" | "system";
  content: string;
  reasoning?: string;
  key_id?: string;
  nonce?: string;
  ciphertext?: string;
  prompt_tokens?: number;
  completion_tokens?: number;
  eval_ms?: number;
  created_at: string;
}

export interface MessageInput {
  role: "user" | "assistant" | "system";
  content?: string;
  reasoning?: string;
  key_id?: string;
  nonce?: string;
  ciphertext?: string;
  prompt_tokens?: number;
  completion_tokens?: number;
  eval_ms?: number;
}

export async function fetchConversations(): Promise<Conversation[]> {
  const body = await request<{ data: Conversation[] }>("/v1/conversations");
  return body.data ?? [];
}

export async function createConversation(
  title: string,
  model: string,
  seal?: { title_nonce: string; title_key_id: string },
): Promise<Conversation> {
  return request<Conversation>("/v1/conversations", {
    method: "POST",
    body: JSON.stringify({ title, model, ...seal }),
  });
}

export async function fetchConversation(
  id: string,
): Promise<{ conversation: Conversation; messages: ConversationMessage[] }> {
  return request<{ conversation: Conversation; messages: ConversationMessage[] }>(`/v1/conversations/${id}`);
}

export async function renameConversation(
  id: string,
  title: string,
  seal?: { title_nonce: string; title_key_id: string },
): Promise<void> {
  await request(`/v1/conversations/${id}`, { method: "PATCH", body: JSON.stringify({ title, ...seal }) });
}

export async function sealMessage(
  conversationId: string,
  messageId: string,
  blob: { key_id: string; nonce: string; ciphertext: string },
): Promise<void> {
  await request(`/v1/conversations/${conversationId}/messages/${messageId}/seal`, {
    method: "POST",
    body: JSON.stringify(blob),
  });
}

export async function deleteConversation(id: string): Promise<void> {
  await request(`/v1/conversations/${id}`, { method: "DELETE" });
}

export async function appendMessages(id: string, messages: MessageInput[]): Promise<void> {
  await request(`/v1/conversations/${id}/messages`, {
    method: "POST",
    body: JSON.stringify({ messages }),
  });
}

/** Аттестация весов: что узел держит и совпадает ли это с манифестом. */
export interface ModelAttestation {
  model: string;
  digest: string;
  expected?: string;
  verified: boolean;
  self_verified: boolean;
  verdict: string;
  reason?: string;
}

export interface WorkerAttestation {
  worker_id: string;
  name: string;
  models: ModelAttestation[];
}

export async function fetchAttestation(): Promise<WorkerAttestation[]> {
  const body = await request<{ data: WorkerAttestation[] }>("/admin/v1/attestation");
  return body.data ?? [];
}

/** Документ базы знаний и найденный фрагмент. */
export interface KnowledgeDocument {
  id: string;
  title: string;
  source: string;
  chunk_count: number;
  created_at: string;
}

export interface KnowledgeHit {
  document_id: string;
  document_title: string;
  ordinal: number;
  content: string;
  similarity: number;
}

export async function fetchDocuments(): Promise<KnowledgeDocument[]> {
  const body = await request<{ data: KnowledgeDocument[] }>("/v1/rag/documents");
  return body.data ?? [];
}

export async function addDocument(input: { title?: string; source?: string; text: string }): Promise<KnowledgeDocument> {
  const body = await request<{ document: KnowledgeDocument }>("/v1/rag/documents", {
    method: "POST",
    body: JSON.stringify(input),
  });
  return body.document;
}

export async function deleteDocument(id: string): Promise<void> {
  await request(`/v1/rag/documents/${id}`, { method: "DELETE" });
}

export async function searchKnowledge(query: string, limit = 5): Promise<KnowledgeHit[]> {
  const body = await request<{ data: KnowledgeHit[] }>("/v1/rag/search", {
    method: "POST",
    body: JSON.stringify({ query, limit }),
  });
  return body.data ?? [];
}

/** Топология: размещение ревизии по узлам (этап шардинга). */
export interface TopologyStage {
  id: string;
  stage_index: number;
  worker_id: string;
  worker_name?: string;
  layer_start: number;
  layer_end: number;
  /** Доля модели на этом узле, 0…1. */
  share: number;
  role: string;
}

export interface TopologyRow {
  id: string;
  revision_id: string;
  model_name?: string;
  version: number;
  status: string;
  coordinator_worker_id?: string;
  split_mode: string;
  /** Почему группа не может подняться: не хватает памяти или узлов. */
  error?: string;
  created_at: string;
  activated_at?: string;
  total_layers: number;
}

export interface TopologyDetail {
  topology: TopologyRow;
  stages: TopologyStage[];
}

/**
 * Версии размещения. Список отдаёт шапку вместе с этапами, поэтому отдельный
 * запрос за деталями нужен только когда этапы менялись после загрузки.
 */
export async function fetchTopologies(): Promise<TopologyDetail[]> {
  const body = await request<{ data: TopologyDetail[] }>("/admin/v1/topologies");
  return body.data ?? [];
}

export async function fetchTopologyDetail(id: string): Promise<TopologyDetail> {
  return request<TopologyDetail>(`/admin/v1/topologies/${id}`);
}

export async function setTopologyStatus(id: string, status: string): Promise<void> {
  await request(`/admin/v1/topologies/${id}/status`, {
    method: "POST",
    body: JSON.stringify({ status }),
  });
}

export async function fetchWorkers(): Promise<WorkerRow[]> {
  const body = await request<{ data: WorkerRow[] }>("/admin/v1/workers");
  return body.data ?? [];
}

export async function setWorkerState(id: string, state: WorkerState): Promise<void> {
  await request(`/admin/v1/workers/${id}/state`, {
    method: "POST",
    body: JSON.stringify({ state }),
  });
}

export async function setWorkerTrust(id: string, trust_class: "private" | "trusted" | "public"): Promise<void> {
  await request(`/admin/v1/workers/${id}/trust`, {
    method: "POST",
    body: JSON.stringify({ trust_class }),
  });
}

export async function deleteWorker(id: string): Promise<void> {
  await request(`/admin/v1/workers/${id}`, { method: "DELETE" });
}

export async function fetchModels(): Promise<ServingModel[]> {
  const body = await request<{ data: ServingModel[] }>("/v1/models");
  return body.data ?? [];
}

export async function fetchJobs(): Promise<JobRow[]> {
  const body = await request<{ data: JobRow[] }>("/admin/v1/jobs");
  return body.data ?? [];
}

export interface EnrollmentToken {
  id: string;
  label: string;
  expires_at: string;
  token: string;
}

/**
 * Выпускает одноразовый токен подключения. Владелец по умолчанию — сам
 * администратор, поэтому user_id не передаём.
 */
export async function fetchOwnNodes(): Promise<WorkerRow[]> {
  const body = await request<{ data: WorkerRow[] }>("/v1/nodes");
  return body.data ?? [];
}

export async function setOwnNodeState(id: string, state: "available" | "paused"): Promise<void> {
  await request(`/v1/nodes/${id}/state`, { method: "POST", body: JSON.stringify({ state }) });
}

export async function createEnrollment(label: string, ttlHours = 24): Promise<EnrollmentToken> {
  return request<EnrollmentToken>("/v1/enrollments", {
    method: "POST",
    body: JSON.stringify({ label, ttl_hours: ttlHours }),
  });
}

export interface ApiKeyIssued {
  id: string;
  name: string;
  scopes: string[];
  token: string;
}

/** Ключ участника для его собственного харнеса: только чтение каталога и чат. */
export async function createApiKey(name: string): Promise<ApiKeyIssued> {
  return request<ApiKeyIssued>("/v1/keys", {
    method: "POST",
    body: JSON.stringify({ name }),
  });
}

/** Приглашение в сеть: то, что участник вставляет в `worker-agent join`. */
export interface Invite {
  network_name?: string;
  control_url: string;
  enrollment_token: string;
  api_key?: string;
  model?: string;
}

/**
 * Кодирует приглашение в одну строку base64 (UTF-8 безопасно).
 * Именно эту строку участник передаёт команде join.
 */
export function encodeInvite(invite: Invite): string {
  const json = JSON.stringify(invite);
  const bytes = new TextEncoder().encode(json);
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replace(/=+$/, "");
}

export interface ChatMessage {
  role: "user" | "assistant";
  content: string;
  reasoning?: string;
}

export interface ChatUsage {
  prompt_tokens?: number;
  completion_tokens?: number;
  total_tokens?: number;
  tokens_per_second?: number;
  eval_ms?: number;
}

export interface StreamChatOptions {
  model: string;
  messages: ChatMessage[];
  maxTokens?: number;
  signal?: AbortSignal;
  onDelta: (content: string, reasoning: string) => void;
  onUsage?: (usage: ChatUsage) => void;
}

/**
 * Стрим ответа модели через Control Plane.
 *
 * Используется fetch + ReadableStream, а не EventSource: нужен POST с телом и
 * заголовком Authorization. Разбор повторяет формат SSE: события разделены
 * пустой строкой, полезная нагрузка — в строках «data: ».
 */
export async function streamChat(options: StreamChatOptions): Promise<void> {
  const token = getToken();
  const headers = new Headers({ "Content-Type": "application/json", Accept: "text/event-stream" });
  if (token) headers.set("Authorization", `Bearer ${token}`);

  const response = await fetch("/api/v1/chat/completions", {
    method: "POST",
    headers,
    signal: options.signal,
    body: JSON.stringify({
      model: options.model,
      stream: true,
      messages: options.messages.map((m) => ({ role: m.role, content: m.content })),
      max_tokens: options.maxTokens,
    }),
  });

  if (!response.ok) {
    let code = "http_error";
    let message = `HTTP ${response.status}`;
    try {
      const body = (await response.json()) as { error?: { code?: string; message?: string } };
      if (body.error?.code) code = body.error.code;
      if (body.error?.message) message = body.error.message;
    } catch {
      // тело не JSON — оставляем статус
    }
    if (response.status === 401 && token) {
      setToken("");
      window.dispatchEvent(new Event("cn-unauthorized"));
    }
    throw new ApiError(response.status, code, message);
  }
  if (!response.body) {
    throw new ApiError(502, "no_body", "Control Plane не прислал поток");
  }

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";

  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });

    let boundary = buffer.indexOf("\n\n");
    while (boundary >= 0) {
      const event = buffer.slice(0, boundary);
      buffer = buffer.slice(boundary + 2);
      handleEvent(event, options);
      boundary = buffer.indexOf("\n\n");
    }
  }
}

function handleEvent(event: string, options: StreamChatOptions): void {
  for (const line of event.split("\n")) {
    if (!line.startsWith("data: ")) continue;
    const payload = line.slice(6);
    if (payload === "[DONE]") return;

    let parsed: {
      error?: { message?: string };
      choices?: { delta?: { content?: string; reasoning_content?: string }; finish_reason?: string | null }[];
      usage?: ChatUsage;
    };
    try {
      parsed = JSON.parse(payload);
    } catch {
      continue;
    }
    if (parsed.error?.message) {
      throw new ApiError(502, "inference_failed", parsed.error.message);
    }
    if (parsed.usage && options.onUsage) {
      options.onUsage(parsed.usage);
    }
    for (const choice of parsed.choices ?? []) {
      const delta = choice.delta ?? {};
      if (delta.content || delta.reasoning_content) {
        options.onDelta(delta.content ?? "", delta.reasoning_content ?? "");
      }
    }
  }
}

/** Человекочитаемые размеры: ГБ с одним знаком после запятой. */
export function formatGB(bytes?: number): string {
  if (!bytes || bytes <= 0) return "—";
  return `${(bytes / 1024 / 1024 / 1024).toFixed(1)} GB`;
}

/** Относительное время вида «2 мин назад». */
export function formatSince(iso?: string): string {
  if (!iso) return "—";
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "—";
  const seconds = Math.max(0, Math.round((Date.now() - then) / 1000));
  if (seconds < 60) return `${seconds} с назад`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes} мин назад`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours} ч назад`;
  return `${Math.round(hours / 24)} д назад`;
}

export const stateLabels: Record<WorkerState, string> = {
  available: "Доступен",
  paused: "Пауза",
  drain: "Drain",
  maintenance: "Обслуживание",
  quarantined: "Карантин",
  offline: "Не в сети",
};

/** Скорость: одно число после запятой, единица рядом. */
export function formatSpeed(tokensPerSecond?: number): string {
  if (!tokensPerSecond || tokensPerSecond <= 0) return "—";
  return `${tokensPerSecond.toFixed(1)} tok/s`;
}

/** Длительность: миллисекунды до секунд, дальше — секунды с десятыми. */
export function formatDuration(ms?: number): string {
  if (!ms || ms <= 0) return "—";
  if (ms < 1000) return `${Math.round(ms)} мс`;
  return `${(ms / 1000).toFixed(1)} с`;
}

/** Токены: пробел как разделитель тысяч, без сокращений. */
export function formatTokens(count?: number): string {
  if (!count || count <= 0) return "0";
  return count.toLocaleString("ru-RU");
}
