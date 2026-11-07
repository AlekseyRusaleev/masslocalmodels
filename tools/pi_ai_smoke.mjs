// Сквозная проверка платформы через настоящий клиент pi-ai — тот же код, что
// использует харнес для маршрутов llm-pi-ai.
//
//     node tools/pi_ai_smoke.mjs <api-ключ>
//
// Скрипт создаёт провайдера «compute-network» с baseURL нашей платформы,
// отправляет запрос с инструментом и печатает поток событий: текст,
// размышления и вызовы инструментов.

import { pathToFileURL } from "node:url";

const token = process.argv[2];
if (!token) {
  console.error("нужен API-ключ платформы");
  process.exit(2);
}

const dist = (file) =>
  pathToFileURL(
    `C:/Users/Admin/AppData/Local/Programs/DeepSeekHarness/harness/node_modules/@deepseek-ai/dsh/node_modules/@earendil-works/pi-ai/dist/${file}`,
  ).href;

process.env.COMPUTE_NETWORK_API_KEY = token;

const { createModels, createProvider, envApiKeyAuth, Type } = await import(dist("index.js"));
const { openAICompletionsApi } = await import(dist("api/openai-completions.lazy.js"));

const baseUrl = "https://api.nshno.tech/v1";

const model = {
  id: "qwen3:8b",
  name: "Qwen3 8B (RTX 5060)",
  api: "openai-completions",
  provider: "compute-network",
  baseUrl,
  reasoning: false,
  input: ["text"],
  cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
  contextWindow: 8192,
  maxTokens: 4096,
};

const provider = createProvider({
  id: "compute-network",
  name: "Compute Network",
  baseUrl,
  auth: { apiKey: envApiKeyAuth("Compute Network key", ["COMPUTE_NETWORK_API_KEY"]) },
  models: [model],
  api: openAICompletionsApi(),
});

const models = createModels();
models.setProvider(provider);
const selected = models.getModel("compute-network", "qwen3:8b");
console.log("модель найдена:", Boolean(selected));

const tools = [
  {
    name: "get_status",
    description: "Вернуть статус узла платформы",
    parameters: Type.Object({ node: Type.String({ description: "Имя узла" }) }),
  },
];

const context = {
  systemPrompt: "Ты — ассистент платформы. Отвечай кратко и вызывай инструменты, когда это уместно.",
  messages: [{ role: "user", content: "Проверь узел owner-pc инструментом get_status.", timestamp: Date.now() }],
  tools,
};

const started = Date.now();
let text = "";
let thinking = "";
const calls = [];
let stopReason = "";

for await (const event of models.stream(selected, context)) {
  switch (event.type) {
    case "text_delta":
      text += event.delta;
      break;
    case "thinking_delta":
      thinking += event.delta;
      break;
    case "toolcall_end":
      calls.push({ name: event.toolCall.name, arguments: event.toolCall.arguments });
      break;
    case "done":
      stopReason = event.message?.stopReason ?? event.stopReason ?? "";
      break;
    case "error":
      console.log("ОШИБКА:", event.error?.message ?? JSON.stringify(event.error));
      break;
    default:
      break;
  }
}

console.log(JSON.stringify({
  stop_reason: stopReason,
  seconds: Math.round((Date.now() - started) / 100) / 10,
  text: text.slice(0, 300),
  thinking_chars: thinking.length,
  tool_calls: calls,
  usage: undefined,
}, null, 2));
