// Мини-обвязка над Chrome DevTools Protocol: запускает headless-браузер, даёт
// evaluate/goto и закрывает его. Нужна, чтобы проверки панели писались без
// повторения plumbing-кода.
import { spawn } from "node:child_process";
import { existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const CHROME_CANDIDATES = [
  process.env.CHROME_PATH,
  "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe",
  "C:\\Program Files (x86)\\Google\\Chrome\\Application\\chrome.exe",
  join(process.env.LOCALAPPDATA ?? "", "Google", "Chrome", "Application", "chrome.exe"),
  "C:\\Program Files\\Microsoft\\Edge\\Application\\msedge.exe",
].filter(Boolean);

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

/** Ищет Chrome. Если не нашёл — это ошибка окружения, а не теста. */
export function findChrome() {
  const path = CHROME_CANDIDATES.find((candidate) => existsSync(candidate));
  if (!path) throw new Error("Chrome не найден: укажите CHROME_PATH");
  return path;
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

async function debuggerUrl(port) {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    try {
      const response = await fetch(`http://127.0.0.1:${port}/json/version`);
      const body = await response.json();
      if (body.webSocketDebuggerUrl) return body.webSocketDebuggerUrl;
    } catch {
      // браузер ещё поднимается
    }
    await sleep(250);
  }
  throw new Error("Chrome не открыл порт отладки");
}

/**
 * Запускает браузер и страницу.
 *
 * Цель страницы создаётся через Target.createTarget на браузерном соединении:
 * HTTP-путь /json/new на Windows иногда отказывает, хотя порт уже слушает.
 */
export async function startBrowser({ port = 9341, prefix = "cn-check", windowSize = "1440,1000" } = {}) {
  const profile = join(tmpdir(), `${prefix}-${Date.now()}`);
  const chrome = spawn(findChrome(), [
    "--headless=new",
    `--remote-debugging-port=${port}`,
    `--user-data-dir=${profile}`,
    "--no-first-run",
    "--no-default-browser-check",
    "--disable-extensions",
    `--window-size=${windowSize}`,
    "about:blank",
  ], { stdio: "ignore" });

  const browser = await connect(await debuggerUrl(port));
  const { targetId } = await browser.send("Target.createTarget", { url: "about:blank" });
  const { sessionId } = await browser.send("Target.attachToTarget", { targetId, flatten: true });
  await browser.send("Page.enable", {}, sessionId);
  await browser.send("Runtime.enable", {}, sessionId);

  const evaluate = async (expression) => {
    const result = await browser.send(
      "Runtime.evaluate",
      { expression, returnByValue: true, awaitPromise: true },
      sessionId,
    );
    if (result.exceptionDetails) {
      throw new Error(result.exceptionDetails.text + " " + (result.exceptionDetails.exception?.description ?? ""));
    }
    return result.result?.value;
  };

  const goto = async (url, waitMs = 2000) => {
    await browser.send("Page.navigate", { url }, sessionId);
    await sleep(waitMs);
  };

  return {
    evaluate,
    goto,
    sleep,
    close() {
      try {
        chrome.kill();
      } catch {
        // браузер уже закрыт
      }
    },
  };
}
