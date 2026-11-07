// Отладка панели: открывает раздел, ловит исключения и ошибки консоли, печатает
// текст страницы. Нужен, когда «страница пустая», а браузер молчит.
//
//   node tools/browser_debug.mjs https://control.nshno.tech email password topology
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

const [, , baseUrl, email, password, route = "topology"] = process.argv;
if (!baseUrl || !email || !password) {
  console.error("usage: node tools/browser_debug.mjs <base-url> <email> <password> [route]");
  process.exit(2);
}

const chromePath = CHROME_CANDIDATES.find((path) => existsSync(path));
if (!chromePath) {
  console.error("Chrome не найден: укажите CHROME_PATH");
  process.exit(2);
}

const PORT = 9337;
const profile = join(tmpdir(), `cn-debug-${Date.now()}`);
const chrome = spawn(chromePath, [
  "--headless=new",
  `--remote-debugging-port=${PORT}`,
  `--user-data-dir=${profile}`,
  "--no-first-run",
  "--no-default-browser-check",
  "--disable-extensions",
  "--window-size=1440,1000",
  "about:blank",
], { stdio: "ignore" });

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function debuggerUrl() {
  for (let attempt = 0; attempt < 80; attempt += 1) {
    try {
      const response = await fetch(`http://127.0.0.1:${PORT}/json/version`);
      const body = await response.json();
      if (body.webSocketDebuggerUrl) return body.webSocketDebuggerUrl;
    } catch {
      // Chrome ещё поднимается
    }
    await sleep(250);
  }
  throw new Error("Chrome не открыл отладку");
}

const wsUrl = await debuggerUrl();
const target = await (await fetch(`http://127.0.0.1:${PORT}/json/new?about:blank`, { method: "PUT" })).json();
const socket = new WebSocket(target.webSocketDebuggerUrl);
await new Promise((resolve) => socket.addEventListener("open", resolve, { once: true }));

let nextId = 1;
const pending = new Map();
const events = [];

socket.addEventListener("message", (event) => {
  const message = JSON.parse(event.data);
  if (message.id && pending.has(message.id)) {
    pending.get(message.id)(message);
    pending.delete(message.id);
    return;
  }
  if (message.method === "Runtime.exceptionThrown") {
    const details = message.params.exceptionDetails;
    events.push({
      type: "exception",
      text: details.text,
      description: details.exception?.description ?? "",
      url: details.url,
    });
  }
  if (message.method === "Runtime.consoleAPICalled" && ["error", "warning"].includes(message.params.type)) {
    events.push({
      type: message.params.type,
      text: message.params.args.map((arg) => arg.value ?? arg.description ?? arg.type).join(" "),
    });
  }
});

function send(method, params = {}) {
  const id = nextId++;
  socket.send(JSON.stringify({ id, method, params }));
  return new Promise((resolve) => pending.set(id, resolve));
}

await send("Page.enable");
await send("Runtime.enable");

async function evaluate(expression) {
  const result = await send("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true });
  if (result.result?.exceptionDetails) {
    return { error: result.result.exceptionDetails.text };
  }
  return { value: result.result?.result?.value };
}

await send("Page.navigate", { url: baseUrl });
await sleep(2000);

// Вход через форму.
await evaluate(`
  (() => {
    const set = (selector, value) => {
      const input = document.querySelector(selector);
      if (!input) return false;
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
      setter.call(input, value);
      input.dispatchEvent(new Event('input', { bubbles: true }));
      return true;
    };
    set('.login-card input[type="email"]', ${JSON.stringify(email)});
    set('.login-card input[type="password"]', ${JSON.stringify(password)});
    document.querySelector('.login-card button[type="submit"]')?.click();
    return true;
  })()
`);
await sleep(3000);

await send("Page.navigate", { url: `${baseUrl}/#${route}` });
await sleep(3000);

const text = await evaluate("document.body.innerText");
const html = await evaluate("document.querySelector('.content-inner')?.innerHTML?.slice(0, 400) ?? 'нет контента'");

console.log(JSON.stringify({ route, text: text.value, html: html.value, events }, null, 2));
socket.close();
chrome.kill();
