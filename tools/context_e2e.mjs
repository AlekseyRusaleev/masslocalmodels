// Сквозная проверка окна контекста через публичный API платформы.
//
// Проверяет две вещи, которые нельзя увидеть в панели:
//   1) скорость и счётчики токенов на боевом маршруте (харнес -> API -> узел);
//   2) что переполнение окна возвращается ошибкой, а не молчаливой обрезкой
//      начала разговора.
//
// Запуск: node tools/context_e2e.mjs
import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

const BASE = process.env.COMPUTE_NETWORK_BASE ?? "https://api.nshno.tech/v1";
const MODEL = process.env.COMPUTE_NETWORK_MODEL ?? "qwen3:8b";

function readKey() {
  if (process.env.COMPUTE_NETWORK_API_KEY) return process.env.COMPUTE_NETWORK_API_KEY;
  const path = join(homedir(), "AppData", "Roaming", "DeepSeekHarness", "data", ".credentials.yaml");
  const text = readFileSync(path, "utf8");
  const match = text.match(/COMPUTE_NETWORK_API_KEY:\s*(\S+)/);
  if (!match) throw new Error("ключ COMPUTE_NETWORK_API_KEY не найден");
  return match[1];
}

const KEY = readKey();

// fillers — сколько символов текста добавить, чтобы получить примерно нужное
// число токенов. Для русского текста Qwen делит примерно 2.5 символа на токен.
function filler(chars) {
  const sentence = "Правило резервирования: узел не должен зависеть от одного канала связи, поэтому маршрут дублируется. ";
  return sentence.repeat(Math.ceil(chars / sentence.length)).slice(0, chars);
}

async function chat(prompt, { maxTokens = 900, stream = true } = {}) {
  const started = Date.now();
  const resp = await fetch(`${BASE}/chat/completions`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${KEY}`,
    },
    body: JSON.stringify({
      model: MODEL,
      stream,
      max_tokens: maxTokens,
      messages: [{ role: "user", content: prompt }],
    }),
  });

  if (!resp.ok) {
    const body = await resp.text();
    return { ok: false, status: resp.status, error: body.slice(0, 400), ms: Date.now() - started };
  }

  if (!stream) {
    const data = await resp.json();
    return { ok: true, ms: Date.now() - started, usage: data.usage, data };
  }

  const reader = resp.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  let content = "";
  let usage = null;
  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    const lines = buffer.split("\n");
    buffer = lines.pop() ?? "";
    for (const line of lines) {
      if (!line.startsWith("data:")) continue;
      const payload = line.slice(5).trim();
      if (!payload || payload === "[DONE]") continue;
      let chunk;
      try {
        chunk = JSON.parse(payload);
      } catch {
        continue;
      }
      if (chunk.error) {
        return { ok: false, status: 200, error: JSON.stringify(chunk.error).slice(0, 400), ms: Date.now() - started };
      }
      const delta = chunk.choices?.[0]?.delta ?? {};
      if (delta.content) content += delta.content;
      if (chunk.usage) usage = chunk.usage;
    }
  }
  return { ok: true, ms: Date.now() - started, usage, content };
}

const results = [];

// 1. Короткий запрос: базовая скорость маршрута.
{
  const r = await chat("Перечисли десять коротких правил проектирования распределённых систем, каждое с новой строки.");
  if (r.ok) {
    const out = r.usage?.completion_tokens ?? 0;
    results.push({
      тест: "короткий запрос",
      результат: "ok",
      токенов_вход: r.usage?.prompt_tokens ?? 0,
      токенов_выход: out,
      время_с: (r.ms / 1000).toFixed(1),
      ток_в_с: out > 0 ? (out / (r.ms / 1000)).toFixed(1) : "—",
    });
  } else {
    results.push({ тест: "короткий запрос", результат: `ошибка ${r.status}`, деталь: r.error });
  }
}

// 2. Промпт около 15000 токенов: должен пройти целиком, начало не потеряно.
{
  const marker = "СЕКРЕТ-В-НАЧАЛЕ-2471";
  const prompt = `${marker}\n\n${filler(37000)}\n\nКакая метка была в самом начале? Ответь одной строкой.`;
  const r = await chat(prompt, { maxTokens: 900 });
  results.push({
    тест: "промпт ~15K токенов",
    результат: r.ok ? "ok" : `ошибка ${r.status}`,
    токенов_вход: r.usage?.prompt_tokens ?? "—",
    токенов_выход: r.usage?.completion_tokens ?? "—",
    время_с: (r.ms / 1000).toFixed(1),
    метка_видна: r.ok ? String(r.content).includes("2471") : "—",
    деталь: r.ok ? undefined : r.error,
  });
}

// 3. Промпт больше окна: платформа обязана сказать об этом, а не отвечать.
{
  const prompt = `${filler(90000)}\n\nСколько правил в тексте выше? Ответь одной строкой.`;
  const r = await chat(prompt, { maxTokens: 900 });
  results.push({
    тест: "промпт ~36K токенов (больше окна)",
    результат: r.ok ? "ok (обрезка не замечена)" : `ошибка ${r.status}`,
    токенов_вход: r.usage?.prompt_tokens ?? "—",
    токенов_выход: r.usage?.completion_tokens ?? "—",
    время_с: (r.ms / 1000).toFixed(1),
    деталь: r.ok ? undefined : r.error,
  });
}

console.table(results);
