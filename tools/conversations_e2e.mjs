// Проверяет историю диалогов через публичный API: создание, добавление хода,
// чтение, переименование, изоляцию между аккаунтами и удаление.
//
//   node tools/conversations_e2e.mjs <admin-email> <admin-password> [other-email] [other-password]
//
// Изоляция проверяется только если передан второй аккаунт: без него тест честно
// сообщает, что проверка прав не выполнялась.
const [, , adminEmail, adminPassword, otherEmail, otherPassword] = process.argv;
if (!adminEmail || !adminPassword) {
  console.error("usage: node tools/conversations_e2e.mjs <admin-email> <admin-password> [other-email] [other-password]");
  process.exit(2);
}

const BASE = process.env.COMPUTE_NETWORK_BASE ?? "https://api.nshno.tech";
const results = [];
const record = (name, ok, detail = "") => {
  results.push({ name, ok, detail });
};

async function api(path, options = {}, token) {
  const headers = new Headers(options.headers ?? {});
  headers.set("Accept", "application/json");
  if (options.body) headers.set("Content-Type", "application/json");
  if (token) headers.set("Authorization", `Bearer ${token}`);
  const response = await fetch(`${BASE}${path}`, { ...options, headers });
  let body = null;
  try {
    body = await response.json();
  } catch {
    // пустое тело
  }
  return { status: response.status, body };
}

async function login(email, password) {
  const { status, body } = await api("/v1/auth/login", {
    method: "POST",
    body: JSON.stringify({ email, password }),
  });
  if (status !== 200 || !body?.token) throw new Error(`вход ${email}: ${status} ${JSON.stringify(body)}`);
  return body.token;
}

const adminToken = await login(adminEmail, adminPassword);

// 1. Создание диалога.
const created = await api(
  "/v1/conversations",
  { method: "POST", body: JSON.stringify({ title: "Проверка истории", model: "qwen3:8b" }) },
  adminToken,
);
record("диалог создан", created.status === 201 && Boolean(created.body?.id), `${created.status}`);
const conversationId = created.body?.id;
if (!conversationId) {
  console.table(results);
  process.exit(1);
}

// 2. Сохранение хода: вопрос и ответ.
const appended = await api(
  `/v1/conversations/${conversationId}/messages`,
  {
    method: "POST",
    body: JSON.stringify({
      messages: [
        {
          role: "user",
          key_id: "key-e2e",
          nonce: "bm9uY2UtZTJlLTEy",
          ciphertext: "c2VhbGVkLXVzZXI=",
        },
        {
          role: "assistant",
          key_id: "key-e2e",
          nonce: "bm9uY2UtZTJlLTEy",
          ciphertext: "c2VhbGVkLWFzc2lzdGFudA==",
          prompt_tokens: 24,
          completion_tokens: 18,
          eval_ms: 260,
        },
      ],
    }),
  },
  adminToken,
);
record("ход сохранён", appended.status === 201 && appended.body?.data?.length === 2, `${appended.status}`);

// 3. Чтение диалога: порядок, размышления и счётчики на месте.
const read = await api(`/v1/conversations/${conversationId}`, {}, adminToken);
const messages = read.body?.messages ?? [];
record("диалог читается", read.status === 200 && messages.length === 2, `${read.status}, сообщений ${messages.length}`);
record(
  "порядок и поля сохранены",
  messages[0]?.role === "user" &&
    messages[1]?.role === "assistant" &&
    messages[1]?.completion_tokens === 18 &&
    messages[0]?.content === "" &&
    messages[1]?.ciphertext === "c2VhbGVkLWFzc2lzdGFudA==",
  `ordinals ${messages.map((message) => message.ordinal).join(",")}`,
);

const plaintext = await api(
  `/v1/conversations/${conversationId}/messages`,
  { method: "POST", body: JSON.stringify({ messages: [{ role: "user", content: "открытый текст" }] }) },
  adminToken,
);
record("открытый текст отвергнут", plaintext.status === 400, `${plaintext.status}`);

// 4. Список содержит диалог с числом сообщений.
const list = await api("/v1/conversations", {}, adminToken);
const row = (list.body?.data ?? []).find((item) => item.id === conversationId);
record("диалог в списке", Boolean(row) && row.message_count === 2, `сообщений ${row?.message_count ?? "—"}`);

// 5. Переименование.
const renamed = await api(
  `/v1/conversations/${conversationId}`,
  { method: "PATCH", body: JSON.stringify({ title: "Новое имя" }) },
  adminToken,
);
record("переименование", renamed.status === 200, `${renamed.status}`);

// 6. Изоляция: второй аккаунт не должен видеть чужой диалог.
if (otherEmail && otherPassword) {
  const otherToken = await login(otherEmail, otherPassword);
  const foreignRead = await api(`/v1/conversations/${conversationId}`, {}, otherToken);
  record("чужой диалог не читается", foreignRead.status === 404, `${foreignRead.status}`);
  const foreignWrite = await api(
    `/v1/conversations/${conversationId}/messages`,
    { method: "POST", body: JSON.stringify({ messages: [{ role: "user", key_id: "k", nonce: "n", ciphertext: "eA==" }] }) },
    otherToken,
  );
  record("в чужой диалог нельзя писать", foreignWrite.status === 404, `${foreignWrite.status}`);
  const foreignDelete = await api(`/v1/conversations/${conversationId}`, { method: "DELETE" }, otherToken);
  record("чужой диалог нельзя удалить", foreignDelete.status === 404, `${foreignDelete.status}`);
} else {
  record("изоляция аккаунтов", false, "не проверялась: не передан второй аккаунт");
}

// 7. Удаление своего диалога.
const removed = await api(`/v1/conversations/${conversationId}`, { method: "DELETE" }, adminToken);
record("диалог удалён", removed.status === 200, `${removed.status}`);
const afterDelete = await api(`/v1/conversations/${conversationId}`, {}, adminToken);
record("после удаления не читается", afterDelete.status === 404, `${afterDelete.status}`);

console.table(results);
process.exit(results.every((result) => result.ok) ? 0 : 1);
