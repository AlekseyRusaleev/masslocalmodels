// Снимает скриншоты страниц панели в headless Chrome.
//
// Зачем: интерфейс нужно проверять на живом браузере, а не на «сборка прошла».
// Скрипт сам поднимает Chrome с удалённой отладкой, кладёт административный
// токен в localStorage нужного origin и делает снимок каждого маршрута.
//
//     node tools/browser_shot.mjs https://control.nshno.tech <token> out
//
// Chrome ищется в стандартных местах Windows; путь можно задать в CHROME_PATH.

import { spawn } from "node:child_process";
import { existsSync, mkdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const CHROME_CANDIDATES = [
  process.env.CHROME_PATH,
  "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe",
  "C:\\Program Files (x86)\\Google\\Chrome\\Application\\chrome.exe",
  join(process.env.LOCALAPPDATA ?? "", "Google", "Chrome", "Application", "chrome.exe"),
  "C:\\Program Files\\Microsoft\\Edge\\Application\\msedge.exe",
].filter(Boolean);

const [, , baseUrl, token, outDir = "shots"] = process.argv;
if (!baseUrl || !token) {
  console.error("usage: node tools/browser_shot.mjs <base-url> <token> [out-dir]");
  process.exit(2);
}

const chromePath = CHROME_CANDIDATES.find((path) => existsSync(path));
if (!chromePath) {
  console.error("Chrome не найден: укажите CHROME_PATH");
  process.exit(2);
}

const ROUTES = [
  ["chat", "Чат"],
  ["nodes", "Узлы"],
  ["models", "Модели"],
  ["jobs", "Задания"],
  ["settings", "Настройки"],
];

const PORT = 9333;
const profile = join(tmpdir(), `cn-shot-${Date.now()}`);
const chrome = spawn(chromePath, [
  "--headless=new",
  `--remote-debugging-port=${PORT}`,
  `--user-data-dir=${profile}`,
  "--no-first-run",
  "--no-default-browser-check",
  "--disable-extensions",
  "--hide-scrollbars",
  "--window-size=1440,1000",
  "about:blank",
], { stdio: "ignore" });

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function debuggerUrl() {
  for (let attempt = 0; attempt < 60; attempt += 1) {
    try {
      const response = await fetch(`http://127.0.0.1:${PORT}/json/version`);
      const body = await response.json();
      if (body.webSocketDebuggerUrl) return body.webSocketDebuggerUrl;
    } catch {
      // Chrome ещё поднимается
    }
    await sleep(250);
  }
  throw new Error("Chrome не открыл порт отладки");
}

class Session {
  constructor(socket) {
    this.socket = socket;
    this.nextId = 1;
    this.pending = new Map();
    socket.addEventListener("message", (event) => {
      const message = JSON.parse(event.data);
      if (message.id && this.pending.has(message.id)) {
        const { resolve, reject } = this.pending.get(message.id);
        this.pending.delete(message.id);
        if (message.error) reject(new Error(JSON.stringify(message.error)));
        else resolve(message.result);
      }
    });
  }

  send(method, params = {}, sessionId) {
    const id = this.nextId++;
    const payload = { id, method, params };
    if (sessionId) payload.sessionId = sessionId;
    this.socket.send(JSON.stringify(payload));
    return new Promise((resolve, reject) => this.pending.set(id, { resolve, reject }));
  }
}

function connect(url) {
  return new Promise((resolve, reject) => {
    const socket = new WebSocket(url);
    socket.addEventListener("open", () => resolve(new Session(socket)));
    socket.addEventListener("error", reject);
  });
}

const session = await connect(await debuggerUrl());
const { targetId } = await session.send("Target.createTarget", { url: "about:blank" });
const { sessionId } = await session.send("Target.attachToTarget", { targetId, flatten: true });

await session.send("Page.enable", {}, sessionId);
await session.send("Runtime.enable", {}, sessionId);

async function goto(url, waitMs) {
  await session.send("Page.navigate", { url }, sessionId);
  await sleep(waitMs);
}

async function evaluate(expression) {
  const result = await session.send("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true }, sessionId);
  return result?.result?.value;
}

// Токен кладём в localStorage того же origin, что и панель.
await goto(baseUrl, 1500);
await evaluate(`localStorage.setItem("cn.admin_token", ${JSON.stringify(token)})`);

mkdirSync(outDir, { recursive: true });
const report = [];

for (const [route, title] of ROUTES) {
  await goto(`${baseUrl}/#${route}`, route === "chat" ? 2200 : 1600);
  const text = await evaluate("document.body.innerText.slice(0, 400)");
  const shot = await session.send("Page.captureScreenshot", { format: "png", captureBeyondViewport: false }, sessionId);
  const file = join(outDir, `${route}.png`);
  writeFileSync(file, Buffer.from(shot.data, "base64"));
  report.push({ route, title, file, preview: (text ?? "").replace(/\s+/g, " ").slice(0, 160) });
}

console.log(JSON.stringify(report, null, 2));
chrome.kill();
process.exit(0);
