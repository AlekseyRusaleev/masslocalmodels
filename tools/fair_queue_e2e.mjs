// Проверяет честную очередь этапа 7: один заказчик не занимает все узлы.
//
// Сценарий: три запроса от первого аккаунта и один от второго, два узла-заглушки.
// Без честной очереди оба узла забрали бы задания первого (FIFO), с ней — по
// одному на каждого: у автора с меньшей нагрузкой приоритет выше.
//
//   node tools/fair_queue_e2e.mjs <admin-email> <admin-password>
const [, , adminEmail, adminPassword] = process.argv;
if (!adminEmail || !adminPassword) {
  console.error("usage: node tools/fair_queue_e2e.mjs <admin-email> <admin-password>");
  process.exit(2);
}

const BASE = process.env.COMPUTE_NETWORK_BASE ?? "https://api.nshno.tech";
const MODEL = "fair-1b:test";
const PASSWORD = "Fair-Queue-9021-Pw";
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
  if (status !== 200 || !body?.token) throw new Error(`вход ${email}: ${status}`);
  return body.token;
}

const admin = await login(adminEmail, adminPassword);

// --- два заказчика ---
const stamp = Date.now();
const userA = `fair-a-${stamp}@nshno.tech`;
const userB = `fair-b-${stamp}@nshno.tech`;
const createdA = await api("/admin/v1/users", {
  method: "POST",
  token: admin,
  body: { email: userA, name: "Fair A", role: "user", password: PASSWORD },
});
const createdB = await api("/admin/v1/users", {
  method: "POST",
  token: admin,
  body: { email: userB, name: "Fair B", role: "user", password: PASSWORD },
});
record("два заказчика созданы", createdA.status === 201 && createdB.status === 201,
  `${createdA.status}/${createdB.status}`);
const tokenA = await login(userA, PASSWORD);
const tokenB = await login(userB, PASSWORD);

// --- два узла-заглушки ---
async function makeStub(name) {
  const enrollment = await api("/admin/v1/enrollments", {
    method: "POST",
    token: admin,
    body: { label: name, ttl_hours: 1 },
  });
  const enrolled = await api("/worker/v1/enroll", {
    method: "POST",
    body: {
      token: enrollment.body?.token,
      name,
      hostname: name,
      os: "linux",
      agent_version: "fair-queue-e2e",
      hardware: { cpu_model: "stub", cpu_cores: 1, ram_bytes: 1, gpus: [], cuda_version: "", driver_version: "" },
    },
  });
  const device = enrolled.body?.device_token;
  const workerID = enrolled.body?.worker_id;
  await api("/worker/v1/heartbeat", {
    method: "POST",
    token: device,
    body: { state: "available", meta: { served_model: MODEL, num_ctx: 2048 } },
  });
  return { name, device, workerID };
}

const stub1 = await makeStub("fair-node-1");
const stub2 = await makeStub("fair-node-2");
record("два узла-заглушки подняты", Boolean(stub1.device && stub2.device), `${stub1.workerID}/${stub2.workerID}`);

function pull(stub, seconds = 25) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), seconds * 1000);
  return api("/worker/v1/jobs/pull", { method: "POST", token: stub.device, signal: controller.signal })
    .finally(() => clearTimeout(timer));
}

async function finish(stub, jobId) {
  const headers = new Headers({ Authorization: `Bearer ${stub.device}`, "Content-Type": "application/x-ndjson" });
  await fetch(`${BASE}/worker/v1/jobs/${jobId}/chunkstream`, {
    method: "POST",
    headers,
    body: `${JSON.stringify({ content: "ок" })}\n`,
  });
  return api(`/worker/v1/jobs/${jobId}/done`, {
    method: "POST",
    token: stub.device,
    body: {
      error: "",
      prompt_tokens: 5,
      completion_tokens: 2,
      eval_ms: 10,
      finish_reason: "stop",
      served_model: MODEL,
      served_digest: "fairtest",
    },
  });
}

function chat(token, content) {
  return fetch(`${BASE}/v1/chat/completions`, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: JSON.stringify({ model: MODEL, stream: false, max_tokens: 16, messages: [{ role: "user", content }] }),
  }).then(async (response) => ({ status: response.status, body: await response.json().catch(() => null) }));
}

// --- очередь: A, A, A, B (именно в таком порядке создаются) ---
const a1 = chat(tokenA, "A-первый");
await sleep(150);
const a2 = chat(tokenA, "A-второй");
await sleep(150);
const a3 = chat(tokenA, "A-третий");
await sleep(150);
const b1 = chat(tokenB, "B-единственный");
await sleep(700);

// --- оба узла забирают одновременно ---
const [claim1, claim2] = await Promise.all([pull(stub1, 20), pull(stub2, 20)]);
const payloadOf = (response) => JSON.stringify(response.body?.payload?.messages ?? response.body ?? {});
const claimed = [payloadOf(claim1), payloadOf(claim2)];
record("первое задание забрано", Boolean(claim1.body?.id), `${claim1.status}`);
record("второе задание забрано", Boolean(claim2.body?.id), `${claim2.status}`);

const gotFromA = claimed.filter((payload) => payload.includes("A-")).length;
const gotFromB = claimed.filter((payload) => payload.includes("B-")).length;
record(
  "очередь честная: один узел занят заказчиком A, второй — заказчиком B",
  gotFromA === 1 && gotFromB === 1,
  `A:${gotFromA}, B:${gotFromB} (${claimed.map((p) => p.replace(/\s+/g, "").slice(0, 40)).join(" | ")})`,
);

// --- закрываем эти два и добираем остальные ---
const firstStub = claimed[0].includes("A-") ? stub1 : stub2;
const secondStub = firstStub === stub1 ? stub2 : stub1;
const firstJob = claimed[0].includes("A-") ? claim1.body.id : claim2.body.id;
const secondJob = claimed[0].includes("A-") ? claim2.body.id : claim1.body.id;
await finish(firstStub, firstJob);
await finish(secondStub, secondJob);

const [rest1, rest2] = await Promise.all([pull(stub1, 20), pull(stub2, 20)]);
record("оставшиеся задания забраны", Boolean(rest1.body?.id) && Boolean(rest2.body?.id),
  `${rest1.status}/${rest2.status}`);
if (rest1.body?.id) await finish(stub1, rest1.body.id);
if (rest2.body?.id) await finish(stub2, rest2.body.id);

const answers = await Promise.all([a1, a2, a3, b1]);
const answered = answers.filter((answer) => answer.status === 200).length;
record("все четыре клиента получили ответ", answered === 4,
  answers.map((a) => a.status).join(", "));

// --- уборка: узлы и заказчики ---
await api("/admin/v1/workers/" + stub1.workerID, { method: "DELETE", token: admin });
await api("/admin/v1/workers/" + stub2.workerID, { method: "DELETE", token: admin });
const removedA = await api(`/admin/v1/users/${createdA.body?.id}`, { method: "DELETE", token: admin });
const removedB = await api(`/admin/v1/users/${createdB.body?.id}`, { method: "DELETE", token: admin });
record("заказчики закрыты", removedA.status === 200 && removedB.status === 200,
  `${removedA.status}/${removedB.status}`);

const relogin = await api("/v1/auth/login", { method: "POST", body: { email: userA, password: PASSWORD } });
record("закрытый аккаунт не входит", relogin.status >= 400, `${relogin.status}`);

console.table(results);
process.exit(results.every((result) => result.ok) ? 0 : 1);
