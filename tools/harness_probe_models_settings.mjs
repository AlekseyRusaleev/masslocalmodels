// Открывает «Настройки → Модели» в панели харнеса и печатает текст страницы.
//
//     node tools/harness_probe_models_settings.mjs "http://127.0.0.1:3099/?token=..."

import { spawn } from "node:child_process";
import { existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const CHROME = [
  process.env.CHROME_PATH,
  "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe",
  "C:\\Program Files (x86)\\Google\\Chrome\\Application\\chrome.exe",
].find((path) => path && existsSync(path));

const tokenUrl = process.argv[2];
if (!CHROME || !tokenUrl) {
  console.error("нужен Chrome и URL с токеном");
  process.exit(2);
}

const PORT = 9338;
const profile = join(tmpdir(), `dsh-models-${Date.now()}`);
const chrome = spawn(CHROME, [
  "--headless=new",
  `--remote-debugging-port=${PORT}`,
  `--user-data-dir=${profile}`,
  "--no-first-run",
  "--no-default-browser-check",
  "--window-size=1600,1200",
  "about:blank",
], { stdio: "ignore" });

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function debuggerUrl() {
  for (let attempt = 0; attempt < 80; attempt += 1) {
    try {
      const body = await (await fetch(`http://127.0.0.1:${PORT}/json/version`)).json();
      if (body.webSocketDebuggerUrl) return body.webSocketDebuggerUrl;
    } catch {
      // ждём
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

const socketUrl = await debuggerUrl();
const session = await new Promise((resolve, reject) => {
  const socket = new WebSocket(socketUrl);
  socket.addEventListener("open", () => resolve(new Session(socket)));
  socket.addEventListener("error", reject);
});

const { targetId } = await session.send("Target.createTarget", { url: "about:blank" });
const { sessionId } = await session.send("Target.attachToTarget", { targetId, flatten: true });
await session.send("Page.enable", {}, sessionId);
await session.send("Runtime.enable", {}, sessionId);

async function evaluate(expression) {
  const result = await session.send(
    "Runtime.evaluate",
    { expression, returnByValue: true, awaitPromise: true },
    sessionId,
  );
  if (result.exceptionDetails) return "ОШИБКА: " + result.exceptionDetails.text;
  return result?.result?.value;
}

async function goto(url, waitMs) {
  await session.send("Page.navigate", { url }, sessionId);
  await sleep(waitMs);
}

await goto(tokenUrl, 2500);
const base = new URL(tokenUrl).origin;
await goto(base, 7000);

const clickByText = async (text) =>
  evaluate(`
    (() => {
      const nodes = [...document.querySelectorAll('button,[role="button"],a,div[role="tab"]')];
      const target = nodes.find((el) => (el.innerText ?? '').trim().startsWith(${JSON.stringify(text)}));
      if (!target) return false;
      target.click();
      return true;
    })()
  `);

const openedSettings = await clickByText("Настройки");
await sleep(2000);
const openedModels = await clickByText("Модели");
await sleep(3000);

const settingsText = await evaluate("document.body.innerText");
console.log("Настройки открыты:", openedSettings, "| Модели открыты:", openedModels);
console.log("---");
console.log((settingsText ?? "").slice(0, 3500));

chrome.kill();
process.exit(0);
