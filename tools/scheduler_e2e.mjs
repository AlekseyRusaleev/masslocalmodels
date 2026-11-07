// Проверяет этап 7 (Scheduler + CU) через публичный API:
//  1. второй узел (заглушка) регистрируется и заявляет СВОЮ модель — задания
//     уходят только тому, кто её обслуживает;
//  2. приоритет обгоняет очередь;
//  3. повторная доставка результата не удваивает начисление (идемпотентный журнал);
//  4. аренда: узел забрал задание и замолчал — задание возвращается в очередь,
//     клиент не ждёт вечно;
//  5. подмена модели: узел назвал чужую модель — задание закрывается ошибкой.
//
// Заглушка нужна потому, что на одной видеокарте два узла не поднять: она
// честно выполняет роль «второго участника» на уровне протокола.
//
//   node tools/scheduler_e2e.mjs <admin-email> <admin-password>
const [, , adminEmail, adminPassword] = process.argv;
if (!adminEmail || !adminPassword) {
  console.error("usage: node tools/scheduler_e2e.mjs <admin-email> <admin-password>");
  process.exit(2);
}

const BASE = process.env.COMPUTE_NETWORK_BASE ?? "https://api.nshno.tech";
const STUB_MODEL = "stub-1b:test";
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
    signal: options.signal,
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

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function login(email, password) {
  const { status, body } = await api("/v1/auth/login", { method: "POST", body: { email, password } });
  if (status !== 200 || !body?.token) throw new Error(`вход: ${status} ${JSON.stringify(body)}`);
  return body.token;
}

const admin = await login(adminEmail, adminPassword);

// --- заглушка второго узла ---
const enrollment = await api("/admin/v1/enrollments", {
  method: "POST",
  token: admin,
  body: { label: "scheduler-e2e", ttl_hours: 1 },
});
record("одноразовый токен выдан", enrollment.status === 200 || enrollment.status === 201, `${enrollment.status}`);

const enrolled = await api("/worker/v1/enroll", {
  method: "POST",
  body: {
    token: enrollment.body?.token,
    name: "stub-node",
    hostname: "stub-node",
    os: "linux",
    agent_version: "scheduler-e2e",
    hardware: { cpu_model: "stub", cpu_cores: 1, ram_bytes: 1, gpus: [], cuda_version: "", driver_version: "" },
  },
});
record("узел-заглушка зарегистрирован", Boolean(enrolled.body?.device_token), `${enrolled.status}`);
const device = enrolled.body?.device_token;
const workerID = enrolled.body?.worker_id;
if (!device) {
  console.table(results);
  process.exit(1);
}

async function heartbeat(state = "available") {
  return api("/worker/v1/heartbeat", {
    method: "POST",
    token: device,
    body: { state, meta: { served_model: STUB_MODEL, num_ctx: 4096, rtt_ms: 5 } },
  });
}

async function pull(seconds = 20) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), seconds * 1000);
  try {
    const response = await api("/worker/v1/jobs/pull", {
      method: "POST",
      token: device,
      signal: controller.signal,
    });
    return response;
  } finally {
    clearTimeout(timer);
  }
}

/** Выполняет задание: поток чанков, затем завершение. */
async function finish(jobId, { servedModel = STUB_MODEL, chunks = ["stub answer"], error = "" } = {}) {
  const ndjson = chunks.map((content) => JSON.stringify({ content })).join("\n") + "\n";
  const headers = new Headers({
    Authorization: `Bearer ${device}`,
    "Content-Type": "application/x-ndjson",
  });
  await fetch(`${BASE}/worker/v1/jobs/${jobId}/chunkstream`, { method: "POST", headers, body: ndjson });
  return api(`/worker/v1/jobs/${jobId}/done`, {
    method: "POST",
    token: device,
    body: {
      error,
      prompt_tokens: 11,
      completion_tokens: 7,
      eval_ms: 123,
      finish_reason: "stop",
      served_model: servedModel,
      served_digest: "stubdigest0000",
    },
  });
}

/** Запрос в чат без ожидания ответа: возвращает промис с результатом. */
function chatAsync(content, { priority = 0, stream = false } = {}) {
  const headers = { Authorization: `Bearer ${admin}`, "Content-Type": "application/json" };
  if (priority) headers["X-Compute-Priority"] = String(priority);
  return fetch(`${BASE}/v1/chat/completions`, {
    method: "POST",
    headers,
    body: JSON.stringify({
      model: STUB_MODEL,
      stream,
      max_tokens: 32,
      messages: [{ role: "user", content }],
    }),
  }).then(async (response) => ({ status: response.status, body: await response.json().catch(() => null) }));
}

// Заглушка доступна и обслуживает только свою модель.
const beat = await heartbeat();
record("heartbeat заглушки принят", beat.status === 200, `${beat.status}`);
const catalog = await api("/v1/models", { token: admin });
const served = (catalog.body?.data ?? []).some((item) => item.id === STUB_MODEL);
record("модель заглушки видна в каталоге", served, JSON.stringify(catalog.body?.data?.map((m) => m.id)));

// --- 3. приоритет обгоняет очередь ---
const lowPriority = chatAsync("низкий приоритет", { priority: 0 });
await sleep(400);
const highPriority = chatAsync("высокий приоритет", { priority: 20 });
await sleep(800);

const firstPull = await pull(15);
const firstJob = firstPull.body;
record("задание получено заглушкой", Boolean(firstJob?.id), `${firstPull.status}`);
const firstPayload = JSON.stringify(firstJob?.payload ?? {});
record(
  "первым выдан приоритетный запрос",
  firstPayload.includes("высокий приоритет"),
  firstPayload.slice(0, 120),
);
await finish(firstJob.id);
const highAnswer = await highPriority;
record("приоритетный ответ дошёл до клиента", highAnswer.status === 200, `${highAnswer.status}`);

const secondPull = await pull(15);
record("второе задание получено", Boolean(secondPull.body?.id), `${secondPull.status}`);
await finish(secondPull.body.id);
const lowAnswer = await lowPriority;
record("обычный ответ дошёл до клиента", lowAnswer.status === 200, `${lowAnswer.status}`);

// --- 2. идемпотентный учёт: завершаем то же задание ещё раз ---
const usageBefore = await api("/v1/usage", { token: admin });
const repeat = await finish(secondPull.body.id);
record("повторное завершение принято", repeat.status === 202, `${repeat.status}`);
const usageAfter = await api("/v1/usage", { token: admin });
record(
  "начисление не удвоилось",
  usageBefore.body?.completion_tokens === usageAfter.body?.completion_tokens,
  `${usageBefore.body?.completion_tokens} → ${usageAfter.body?.completion_tokens}`,
);
record("учёт идёт по журналу", (usageAfter.body?.jobs ?? 0) > 0, `заданий в учёте: ${usageAfter.body?.jobs}`);

// --- 4. аренда: узел замолчал до первого токена ---
const lost = chatAsync("задание, которое узел бросит");
await sleep(500);
const claimed = await pull(15);
record("задание для потеряшки получено", Boolean(claimed.body?.id), `${claimed.status}`);

// Ничего не отправляем: ждём, пока аренда истечёт и сборщик вернёт задание.
let returned = null;
for (let attempt = 0; attempt < 40; attempt += 1) {
  await sleep(5000);
  const status = await api(`/v1/jobs/${claimed.body.id}`, { token: admin });
  if (status.body?.state === "queued") {
    returned = status.body;
    break;
  }
  if (status.body?.state === "failed") break;
}
record("аренда истекла, задание вернулось в очередь", returned?.state === "queued",
  returned ? `state=${returned.state}, attempts=${returned.attempts}` : "не вернулось за 200 с");

// Повторная выдача: забираем задание ещё раз и закрываем нормально.
const retried = await pull(15);
record("задание выдано повторно", retried.body?.id === claimed.body.id,
  `${retried.body?.id} против ${claimed.body.id}`);
await finish(retried.body.id);
const lostAnswer = await lost;
record("клиент получил ответ после повтора", lostAnswer.status === 200, `${lostAnswer.status}`);

// --- 5. подмена модели ---
const substituted = chatAsync("ответ должен быть отвергнут");
await sleep(500);
const subJob = await pull(15);
record("задание для проверки подмены получено", Boolean(subJob.body?.id), `${subJob.status}`);
await finish(subJob.body.id, { servedModel: "another-model:7b" });
const subAnswer = await substituted;
record(
  "подмена модели отклонена",
  subAnswer.status !== 200 && JSON.stringify(subAnswer.body ?? {}).includes("model substitution"),
  `${subAnswer.status} ${JSON.stringify(subAnswer.body ?? {}).slice(0, 160)}`,
);

// Убираем заглушку из сети: пауза, чтобы задания на неё больше не приходили.
await heartbeat("paused");
const removal = await api(`/admin/v1/workers/${workerID}`, { method: "DELETE", token: admin });
record("узел-заглушка удалён", removal.status === 200, `${removal.status}`);

console.table(results);
process.exit(results.every((result) => result.ok) ? 0 : 1);
