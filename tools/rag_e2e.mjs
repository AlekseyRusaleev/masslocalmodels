// Проверяет базу знаний: загрузку документа, нарезку на чанки, поиск по
// векторам, изоляцию между аккаунтами и удаление.
//
//   node tools/rag_e2e.mjs <admin-email> <admin-password>
const [, , adminEmail, adminPassword] = process.argv;
if (!adminEmail || !adminPassword) {
  console.error("usage: node tools/rag_e2e.mjs <admin-email> <admin-password>");
  process.exit(2);
}

const BASE = process.env.COMPUTE_NETWORK_BASE ?? "https://api.nshno.tech";
const ADMIN_EMAIL = adminEmail;
const ADMIN_PASSWORD = adminPassword;
const OTHER_EMAIL = process.env.RAG_OTHER_EMAIL ?? "";
const OTHER_PASSWORD = process.env.RAG_OTHER_PASSWORD ?? "";
const results = [];
const record = (name, ok, detail = "") => {
  results.push({ name, ok, detail });
  if (!ok) console.error(`ПРОВАЛ: ${name} — ${detail}`);
};

async function api(path, options = {}) {
  const headers = new Headers(options.headers ?? {});
  headers.set("Accept", "application/json");
  if (options.body) headers.set("Content-Type", "application/json");
  if (options.token) headers.set("Authorization", `Bearer ${options.token}`);
  const response = await fetch(`${BASE}${path}`, {
    method: options.method ?? "GET",
    headers,
    body: options.body ? JSON.stringify(options.body) : undefined,
  });
  const text = await response.text();
  let body = null;
  try {
    body = text ? JSON.parse(text) : null;
  } catch {
    body = { raw: text };
  }
  return { status: response.status, body };
}

const login = await api("/v1/auth/login", { method: "POST", body: { email: ADMIN_EMAIL, password: ADMIN_PASSWORD } });
if (login.status !== 200) throw new Error(`вход: ${login.status}`);
const token = login.body.token;

// Документ с двумя темами: поиск должен находить нужную часть.
const document = {
  title: "Инцидент с очередью заданий",
  source: "runbook",
  text: [
    "Раздел 1. Симптомы.",
    "Ночью очередь заданий перестала продвигаться: задания висели в состоянии streaming, клиенты ждали ответа до таймаута.",
    "Причина оказалась в узле, который забрал задание и замолчал: соединение с ним пропало, а аренда задания ещё не истекла.",
    "Раздел 2. Что делать.",
    "Сначала проверяют журнал заданий: там видно, какой узел взял задание и когда его последний heartbeat.",
    "Затем смотрят аренду: если lease_expires_at в прошлом, сборщик вернёт задание в очередь автоматически.",
    "Если задание уже отдало первые токены, повтор опасен: клиент получил бы склейку двух ответов, поэтому такое задание закрывается ошибкой.",
    "Раздел 3. Профилактика.",
    "Узлам с нестабильной связью ставят меньший приоритет и включают ограничение частоты, чтобы они не забирали больше работы, чем могут доставить.",
    "После исправления сняли метрики: количество заданий в очереди и число возвратов после истечения аренды.",
  ].join("\n\n"),
};

const created = await api("/v1/rag/documents", { method: "POST", token, body: document });
const documentId = created.body?.document?.id;
const chunkCount = created.body?.document?.chunk_count ?? 0;
record("документ загружен", created.status === 201 && Boolean(documentId), `${created.status}`);
record("текст разбит на чанки", chunkCount >= 2, `чанков ${chunkCount}`);
record("векторы посчитаны узлом", Boolean(created.body?.model), created.body?.model ?? "модель не указана");

// --- поиск ---
async function search(query) {
  const response = await api("/v1/rag/search", { method: "POST", token, body: { query, limit: 3 } });
  return { status: response.status, hits: response.body?.data ?? [] };
}

const leaseSearch = await search("что делать, если аренда задания истекла и узел замолчал");
record("поиск вернул результаты", leaseSearch.status === 200 && leaseSearch.hits.length > 0,
  `${leaseSearch.status}, найдено ${leaseSearch.hits.length}`);
const leaseTop = leaseSearch.hits[0]?.content ?? "";
record("верхний результат — про аренду и возврат в очередь",
  /аренд|lease|очеред/i.test(leaseTop), leaseTop.slice(0, 90).replace(/\s+/g, " "));
record("близость посчитана", (leaseSearch.hits[0]?.similarity ?? 0) > 0.2,
  `similarity=${leaseSearch.hits[0]?.similarity?.toFixed(3)}`);

const rateSearch = await search("ограничение частоты и приоритет узлов");
const rateText = rateSearch.hits.map((hit) => hit.content).join(" ");
record("второй запрос находит свою тему", /частот|приоритет/i.test(rateText),
  rateSearch.hits[0]?.content?.slice(0, 80) ?? "пусто");

// --- список документов ---
const list = await api("/v1/rag/documents", { token });
record("документ в списке", (list.body?.data ?? []).some((item) => item.id === documentId),
  `документов ${(list.body?.data ?? []).length}`);

// --- изоляция аккаунтов ---
if (OTHER_EMAIL && OTHER_PASSWORD) {
  const otherLogin = await api("/v1/auth/login", { method: "POST", body: { email: OTHER_EMAIL, password: OTHER_PASSWORD } });
  const otherToken = otherLogin.body?.token;
  const otherList = await api("/v1/rag/documents", { token: otherToken });
  record("чужой документ не виден", !(otherList.body?.data ?? []).some((item) => item.id === documentId),
    `у другого аккаунта документов: ${(otherList.body?.data ?? []).length}`);
  const otherSearch = await api("/v1/rag/search", { method: "POST", token: otherToken, body: { query: "аренда задания" } });
  record("чужой поиск ничего не находит", (otherSearch.body?.data ?? []).length === 0,
    `найдено ${(otherSearch.body?.data ?? []).length}`);
  const otherDelete = await api(`/v1/rag/documents/${documentId}`, { method: "DELETE", token: otherToken });
  record("чужой документ нельзя удалить", otherDelete.status === 404, `${otherDelete.status}`);
} else {
  record("изоляция аккаунтов", false, "не проверялась: не передан второй аккаунт");
}

// --- удаление ---
const removed = await api(`/v1/rag/documents/${documentId}`, { method: "DELETE", token });
record("документ удалён", removed.status === 200, `${removed.status}`);
// Проверяем именно удалённый документ: у аккаунта могут быть другие документы,
// и «поиск вообще пуст» было бы неверной проверкой.
const afterDelete = await search("аренда задания");
const leftover = afterDelete.hits.filter((hit) => hit.document_id === documentId);
record("после удаления документ не находится", leftover.length === 0,
  `совпадений из удалённого документа: ${leftover.length}`);

console.table(results);
process.exit(results.every((result) => result.ok) ? 0 : 1);
