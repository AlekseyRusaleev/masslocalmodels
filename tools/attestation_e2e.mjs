// Проверяет аттестацию весов по подписанному манифесту:
//  1. подписанный манифест + digest узла → задание подтверждено;
//  2. манифест не подписан → задание проходит, но не подтверждено (вердикт
//     unsigned_manifest), потому что «проверено» без подписи было бы ложью;
//  3. подпись ломается, если после подписи изменить файлы;
//  4. подмена весов → задание отклоняется;
//  5. многофайловая ревизия: сверяются хэши каждого файла.
//
//   node tools/attestation_e2e.mjs <admin-email> <admin-password>
const [, , adminEmail, adminPassword] = process.argv;
if (!adminEmail || !adminPassword) {
  console.error("usage: node tools/attestation_e2e.mjs <admin-email> <admin-password>");
  process.exit(2);
}

const BASE = process.env.COMPUTE_NETWORK_BASE ?? "https://api.nshno.tech";
const REAL_MODEL = process.env.COMPUTE_NETWORK_MODEL ?? "qwen3:8b";
const STUB_MODEL = "attest-1b:test";
const SHARDED_MODEL = "attest-shard:test";
const MATCH_DIGEST = "b".repeat(64);
const WRONG_DIGEST = "c".repeat(64);
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
  if (status !== 200 || !body?.token) throw new Error(`вход: ${status}`);
  return body.token;
}

const admin = await login(adminEmail, adminPassword);

/** Заводит ревизию с файлами. Возвращает её id. */
async function registerRevision(name, files) {
  const created = await api("/admin/v1/models", {
    method: "POST",
    token: admin,
    body: {
      name,
      display_name: name,
      source: "attestation-e2e",
      revision: files[0].sha256.slice(0, 12),
      license: "test",
      license_checked: true,
      quantization: "Q4_K_M",
      params_billions: name === REAL_MODEL ? 8 : 1,
    },
  });
  const revisionId = created.body?.revision?.id;
  if (!revisionId) throw new Error(`ревизия не создана: ${created.status}`);
  const artifacts = await api(`/admin/v1/models/${revisionId}/artifacts`, {
    method: "POST",
    token: admin,
    body: {
      artifacts: files.map((file) => ({
        name: file.name,
        url: "https://example.invalid/" + file.name,
        sha256: file.sha256,
        size_bytes: 1,
      })),
    },
  });
  await api(`/admin/v1/models/${revisionId}/status`, {
    method: "POST",
    token: admin,
    body: { status: "active" },
  });
  return { revisionId, artifacts: artifacts.status };
}

async function sign(revisionId) {
  return api(`/admin/v1/models/${revisionId}/sign`, { method: "POST", token: admin });
}

/** Задание в журнале по id. */
async function jobById(id) {
  const jobs = await api("/admin/v1/jobs", { token: admin });
  return (jobs.body?.data ?? []).find((job) => job.id === id);
}

// --- 1. Настоящая модель: подписанный манифест ---
const attestationBefore = await api("/admin/v1/attestation", { token: admin });
const served = (attestationBefore.body?.data?.[0]?.models ?? []).find((model) => model.model === REAL_MODEL);
record("узел сообщил digest весов", Boolean(served?.digest) && served.digest.length === 64,
  served?.digest ?? "нет digest");

if (served?.digest) {
  // Ревизия могла быть создана раньше: тогда просто подписываем существующую.
  const real = await registerRevision(REAL_MODEL, [{ name: "qwen3-8b-Q4_K_M.gguf", sha256: served.digest }]);
  const signed = await sign(real.revisionId);
  record("манифест настоящей модели подписан", signed.status === 200 && Boolean(signed.body?.signature),
    `${signed.status}`);
}

const realChat = await api("/v1/chat/completions", {
  method: "POST",
  token: admin,
  body: {
    model: REAL_MODEL,
    stream: false,
    max_tokens: 900,
    messages: [{ role: "user", content: "Ответь одним словом: подтверждение?" }],
  },
});
record("ответ настоящей модели получен", realChat.status === 200, `${realChat.status}`);
await sleep(800);
const realJob = (await api("/admin/v1/jobs", { token: admin })).body?.data
  ?.find((job) => job.model === REAL_MODEL && job.state === "done");
record("задание подтверждено подписанным манифестом",
  realJob?.digest_verified === true && realJob?.attestation === "verified",
  `digest_verified=${realJob?.digest_verified}, вердикт=${realJob?.attestation}`);

const attestationAfter = await api("/admin/v1/attestation", { token: admin });
const verifiedModel = (attestationAfter.body?.data ?? [])[0]?.models?.find((model) => model.model === REAL_MODEL);
record("аттестация узла подтверждает веса",
  verifiedModel?.verified === true && verifiedModel?.self_verified === true,
  `verified=${verifiedModel?.verified}, самопроверка=${verifiedModel?.self_verified}`);

// --- узел-заглушка для остальных случаев ---
const enrollment = await api("/admin/v1/enrollments", {
  method: "POST",
  token: admin,
  body: { label: "attestation-e2e", ttl_hours: 1 },
});
const enrolled = await api("/worker/v1/enroll", {
  method: "POST",
  body: {
    token: enrollment.body?.token,
    name: "attest-node",
    hostname: "attest-node",
    os: "linux",
    agent_version: "attestation-e2e",
    hardware: { cpu_model: "stub", cpu_cores: 1, ram_bytes: 1, gpus: [], cuda_version: "", driver_version: "" },
  },
});
const device = enrolled.body?.device_token;
const workerID = enrolled.body?.worker_id;
record("узел-заглушка поднят", Boolean(device), `${enrolled.status}`);

async function announce(model) {
  await api("/worker/v1/heartbeat", {
    method: "POST",
    token: device,
    body: { state: "available", meta: { served_model: model, num_ctx: 2048, model_digests: { [model]: MATCH_DIGEST } } },
  });
}

async function pull(seconds = 15) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), seconds * 1000);
  try {
    return await api("/worker/v1/jobs/pull", { method: "POST", token: device, signal: controller.signal });
  } finally {
    clearTimeout(timer);
  }
}

async function finish(jobId, { servedModel, servedDigest, servedFiles } = {}) {
  const headers = new Headers({ Authorization: `Bearer ${device}`, "Content-Type": "application/x-ndjson" });
  await fetch(`${BASE}/worker/v1/jobs/${jobId}/chunkstream`, {
    method: "POST",
    headers,
    body: `${JSON.stringify({ content: "ok" })}\n`,
  });
  return api(`/worker/v1/jobs/${jobId}/done`, {
    method: "POST",
    token: device,
    body: {
      error: "",
      prompt_tokens: 3,
      completion_tokens: 1,
      eval_ms: 5,
      finish_reason: "stop",
      served_model: servedModel,
      served_digest: servedDigest,
      served_files: servedFiles,
    },
  });
}

function chat(model, content) {
  return fetch(`${BASE}/v1/chat/completions`, {
    method: "POST",
    headers: { Authorization: `Bearer ${admin}`, "Content-Type": "application/json" },
    body: JSON.stringify({ model, stream: false, max_tokens: 16, messages: [{ role: "user", content }] }),
  }).then(async (response) => ({ status: response.status, body: await response.json().catch(() => null) }));
}

/** Один прогон: запрос в чат, выдача заглушке, завершение, ответ клиенту. */
async function runJob(model, text, completion) {
  const call = chat(model, text);
  await sleep(500);
  const pulled = await pull();
  if (!pulled.body?.id) return { pulled, answer: { status: 0, body: null } };
  await finish(pulled.body.id, completion);
  const answer = await call;
  return { pulled, answer, job: await jobById(pulled.body.id) };
}

// --- 2. Манифест без подписи ---
await announce(STUB_MODEL);
const unsigned = await registerRevision(STUB_MODEL, [{ name: "attest-1b.gguf", sha256: MATCH_DIGEST }]);
const unsignedRun = await runJob(STUB_MODEL, "манифест не подписан", {
  servedModel: STUB_MODEL,
  servedDigest: MATCH_DIGEST,
});
record("неподписанный манифест не подтверждает веса",
  unsignedRun.answer.status === 200 && unsignedRun.job?.digest_verified === false &&
    unsignedRun.job?.attestation === "unsigned_manifest",
  `ответ ${unsignedRun.answer.status}, вердикт ${unsignedRun.job?.attestation}`);

// --- 3. Подписали — веса подтверждаются ---
const stubSigned = await sign(unsigned.revisionId);
record("манифест заглушки подписан", stubSigned.status === 200, `${stubSigned.status}`);
const signedRun = await runJob(STUB_MODEL, "манифест подписан", {
  servedModel: STUB_MODEL,
  servedDigest: MATCH_DIGEST,
});
record("подписанный манифест подтверждает веса",
  signedRun.answer.status === 200 && signedRun.job?.digest_verified === true &&
    signedRun.job?.attestation === "verified",
  `ответ ${signedRun.answer.status}, вердикт ${signedRun.job?.attestation}`);

// --- 4. Подмена весов ---
const wrongRun = await runJob(STUB_MODEL, "подмена весов", {
  servedModel: STUB_MODEL,
  servedDigest: WRONG_DIGEST,
});
const wrongText = JSON.stringify(wrongRun.answer.body ?? {});
record("подмена весов отклонена",
  wrongRun.answer.status !== 200 && wrongText.includes("weight attestation failed"),
  `${wrongRun.answer.status} ${wrongText.slice(0, 150)}`);

// --- 5. Подпись ломается при изменении файлов ---
const tampered = await api(`/admin/v1/models/${unsigned.revisionId}/artifacts`, {
  method: "POST",
  token: admin,
  body: {
    artifacts: [{ name: "attest-1b-extra.gguf", url: "https://example.invalid/extra", sha256: "d".repeat(64), size_bytes: 1 }],
  },
});
record("файл добавлен после подписи", tampered.status === 201, `${tampered.status}`);
const tamperedRun = await runJob(STUB_MODEL, "подпись испорчена", {
  servedModel: STUB_MODEL,
  servedDigest: MATCH_DIGEST,
});
const tamperedText = JSON.stringify(tamperedRun.answer.body ?? {});
record("испорченная подпись обнаружена",
  tamperedRun.answer.status !== 200 && tamperedText.includes("signature does not match"),
  `${tamperedRun.answer.status} ${tamperedText.slice(0, 150)}`);

// --- 6. Многофайловая ревизия: хэши каждого файла ---
const shardOne = "1".repeat(64);
const shardTwo = "2".repeat(64);
await announce(SHARDED_MODEL);
const sharded = await registerRevision(SHARDED_MODEL, [
  { name: "shard-00001-of-00002.gguf", sha256: shardOne },
  { name: "shard-00002-of-00002.gguf", sha256: shardTwo },
]);
const shardedSigned = await sign(sharded.revisionId);
record("многофайловый манифест подписан", shardedSigned.status === 200, `${shardedSigned.status}`);

const shardedOk = await runJob(SHARDED_MODEL, "верные шарды", {
  servedModel: SHARDED_MODEL,
  servedDigest: MATCH_DIGEST,
  servedFiles: { "shard-00001-of-00002.gguf": shardOne, "shard-00002-of-00002.gguf": shardTwo },
});
record("многофайловая ревизия подтверждена по каждому файлу",
  shardedOk.answer.status === 200 && shardedOk.job?.digest_verified === true &&
    shardedOk.job?.attestation === "verified",
  `ответ ${shardedOk.answer.status}, вердикт ${shardedOk.job?.attestation}`);

const shardedBad = await runJob(SHARDED_MODEL, "чужой шард", {
  servedModel: SHARDED_MODEL,
  servedDigest: MATCH_DIGEST,
  servedFiles: { "shard-00001-of-00002.gguf": shardOne, "shard-00002-of-00002.gguf": "9".repeat(64) },
});
const shardedBadText = JSON.stringify(shardedBad.answer.body ?? {});
record("чужой шард отклонён",
  shardedBad.answer.status !== 200 && shardedBadText.includes("files do not match"),
  `${shardedBad.answer.status} ${shardedBadText.slice(0, 150)}`);

const shardedNoFiles = await runJob(SHARDED_MODEL, "без отчёта о файлах", {
  servedModel: SHARDED_MODEL,
  servedDigest: MATCH_DIGEST,
});
record("без отчёта о файлах многофайловая ревизия не подтверждается",
  shardedNoFiles.answer.status === 200 && shardedNoFiles.job?.digest_verified === false &&
    shardedNoFiles.job?.attestation === "not_reported",
  `ответ ${shardedNoFiles.answer.status}, вердикт ${shardedNoFiles.job?.attestation}`);

// --- уборка ---
await api(`/admin/v1/workers/${workerID}`, { method: "DELETE", token: admin });

console.table(results);
process.exit(results.every((result) => result.ok) ? 0 : 1);
