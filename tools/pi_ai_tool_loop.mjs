// Полный цикл агента через настоящий клиент pi-ai: запрос → вызов инструмента →
// результат инструмента → финальный ответ. Тот же код, что в харнесе.
//
//     node tools/pi_ai_tool_loop.mjs <api-ключ>

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

const tools = [
  {
    name: "get_weather",
    description: "Погода в городе",
    parameters: Type.Object({ city: Type.String({ description: "Город" }) }),
  },
];

const context = {
  systemPrompt: "Ты — ассистент. Когда нужен факт о погоде, вызывай инструмент get_weather.",
  messages: [{ role: "user", content: "Какая сейчас погода в Москве?", timestamp: Date.now() }],
  tools,
};

// Шаг 1: модель просит инструмент.
const first = await models.complete(selected, context);
const call = first.content.find((block) => block.type === "toolCall");
console.log("шаг 1 — stopReason:", first.stopReason, "| инструмент:", call ? `${call.name}(${JSON.stringify(call.arguments)})` : "нет");

if (!call) {
  console.log("модель не вызвала инструмент, дальше проверять нечего");
  process.exit(1);
}

// Шаг 2: клиент возвращает результат инструмента и ждёт ответ словами.
context.messages.push({
  role: "toolResult",
  toolCallId: call.id,
  toolName: call.name,
  content: [{ type: "text", text: JSON.stringify({ temp_c: -3, summary: "снег" }) }],
  isError: false,
  timestamp: Date.now(),
});

const second = await models.complete(selected, context);
const text = second.content
  .filter((block) => block.type === "text")
  .map((block) => block.text)
  .join("");

console.log("шаг 2 — stopReason:", second.stopReason);
console.log("ответ клиенту:", text.replace(/\s+/g, " ").slice(0, 300));
console.log("usage:", JSON.stringify(second.usage ?? {}));
