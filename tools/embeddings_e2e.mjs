// Проверяет /v1/embeddings через платформу:
//  1. одиночный вход → один вектор нужной размерности;
//  2. массив входов → векторы по порядку индексов;
//  3. смысловая близость: «кот» ближе к «кошка», чем к «трактор»;
//  4. счётчики токенов и запись задания в журнал;
//  5. ограничение частоты: после всплеска приходит 429 с Retry-After.
//
//   node tools/embeddings_e2e.mjs <admin-email> <admin-password>
const [, , adminEmail, adminPassword] = process.argv;
if (!adminEmail || !adminPassword) {
  console.error("usage: node tools/embeddings_e2e.mjs <admin-email> <admin-password>");
  process.exit(2);
}

const BASE = process.env.COMPUTE_NETWORK_BASE ?? "https://api.nshno.tech";
const MODEL = process.env.COMPUTE_NETWORK_EMBED_MODEL ?? "nomic-embed-text:latest";
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
  return { status: response.status, body, headers: response.headers };
}

const login = await api("/v1/auth/login", { method: "POST", body: { email: adminEmail, password: adminPassword } });
if (login.status !== 200) throw new Error(`вход: ${login.status}`);
const token = login.body.token;

// --- 1. Одиночный вход ---
const single = await api("/v1/embeddings", {
  method: "POST",
  token,
  body: { model: MODEL, input: "кот сидит на подоконнике" },
});
const singleVector = single.body?.data?.[0]?.embedding ?? [];
record("одиночный вход: вектор получен", single.status === 200 && singleVector.length > 0,
  `${single.status}, размерность ${singleVector.length}`);
record("счётчики токенов пришли", (single.body?.usage?.prompt_tokens ?? 0) > 0,
  `prompt_tokens=${single.body?.usage?.prompt_tokens}`);

// --- 2. Массив входов ---
const batch = await api("/v1/embeddings", {
  method: "POST",
  token,
  body: { model: MODEL, input: ["a cat is sleeping", "a tractor ploughs a field", "a kitten plays with a ball"] },
});
const vectors = batch.body?.data ?? [];
record("массив входов: векторы по порядку",
  batch.status === 200 && vectors.length === 3 && vectors.every((item, index) => item.index === index),
  `${batch.status}, векторов ${vectors.length}`);
record("размерности совпадают",
  vectors.length > 0 && new Set(vectors.map((item) => item.embedding.length)).size === 1,
  `размерность ${vectors[0]?.embedding?.length}`);

// --- 3. Смысловая близость ---
function cosine(a, b) {
  let dot = 0;
  let normA = 0;
  let normB = 0;
  for (let index = 0; index < a.length; index += 1) {
    dot += a[index] * b[index];
    normA += a[index] * a[index];
    normB += b[index] * b[index];
  }
  return dot / (Math.sqrt(normA) * Math.sqrt(normB) || 1);
}

if (vectors.length === 3) {
  const [cat, tractor, kitten] = vectors.map((item) => item.embedding);
  const catKitten = cosine(cat, kitten);
  const catTractor = cosine(cat, tractor);
  record("смысл: «кошка» ближе «котёнка», чем «трактора»", catKitten > catTractor,
    `кошка=${catKitten.toFixed(3)}, трактор=${catTractor.toFixed(3)}`);
}

// --- 4. Задание в журнале ---
const jobs = await api("/admin/v1/jobs", { token });
const embeddingJobs = (jobs.body?.data ?? []).filter((job) => job.kind === "embedding");
record("задания эмбеддингов в журнале", embeddingJobs.length >= 2,
  `найдено ${embeddingJobs.length}`);


// --- 5. Манифест модели эмбеддингов: веса подтверждаются так же, как у чата ---
const worker = await api("/admin/v1/workers", { token });
const servedEmbedding = (worker.body?.data ?? []).find((row) => row.served_embedding_model === MODEL)
  ?.served_embedding_model;
record("узел объявил модель эмбеддингов", servedEmbedding === MODEL, servedEmbedding ?? "не объявлена");

if (servedEmbedding) {
  const attestation = await api("/admin/v1/attestation", { token });
  const digest = (attestation.body?.data ?? [])
    .flatMap((row) => row.models ?? [])
    .find((model) => model.model === MODEL)?.digest;
  record("узел сообщил digest весов эмбеддингов", Boolean(digest) && digest.length === 64, digest ?? "нет digest");

  if (digest) {
    const created = await api("/admin/v1/models", {
      method: "POST",
      token,
      body: {
        name: MODEL,
        display_name: "Nomic Embed Text",
        source: "ollama",
        revision: digest.slice(0, 12),
        license: "Apache-2.0",
        license_checked: true,
        quantization: "F16",
      },
    });
    const revisionId = created.body?.revision?.id;
    await api(`/admin/v1/models/${revisionId}/artifacts`, {
      method: "POST",
      token,
      body: {
        artifacts: [{ name: "nomic-embed-text.gguf", url: "https://example.invalid/nomic.gguf", sha256: digest, size_bytes: 1 }],
      },
    });
    // Ревизия должна быть активной: черновик платформа не раздаёт и не сверяет.
    await api(`/admin/v1/models/${revisionId}/status`, { method: "POST", token, body: { status: "active" } });
    const signed = await api(`/admin/v1/models/${revisionId}/sign`, { method: "POST", token });
    record("манифест эмбеддингов подписан", signed.status === 200, `${signed.status}`);

    const again = await api("/v1/embeddings", { method: "POST", token, body: { model: MODEL, input: "attested" } });
    record("эмбеддинг с манифестом получен", again.status === 200, `${again.status}`);

    const afterJobs = await api("/admin/v1/jobs", { token });
    const attested = (afterJobs.body?.data ?? []).find((job) => job.kind === "embedding" && job.state === "done");
    record("задание эмбеддингов сверено по манифесту",
      attested?.digest_verified === true && attested?.attestation === "verified",
      `вердикт=${attested?.attestation}, job=${attested?.id?.slice(0, 8) ?? "—"}`);
  }
}

// --- 6. Ограничение частоты ---
const burst = await Promise.all(
  Array.from({ length: 40 }, (_, index) =>
    fetch(`${BASE}/v1/embeddings`, {
      method: "POST",
      headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
      body: JSON.stringify({ model: MODEL, input: `проверка лимита ${index}` }),
    }).then((response) => ({ status: response.status, retryAfter: response.headers.get("retry-after") })),
  ),
);
const limited = burst.filter((item) => item.status === 429);
record("лишние запросы упираются в лимит", limited.length > 0,
  `429: ${limited.length} из ${burst.length}, Retry-After ${limited[0]?.retryAfter ?? "—"}`);
record("в отказе есть Retry-After", Boolean(limited[0]?.retryAfter), `Retry-After=${limited[0]?.retryAfter}`);

console.table(results);
process.exit(results.every((result) => result.ok) ? 0 : 1);
