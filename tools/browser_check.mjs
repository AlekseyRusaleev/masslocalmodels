// Проверяет панель в настоящем браузере: не «сборка прошла», а «страница
// работает и данные пришли».
//
//     node tools/browser_check.mjs https://control.nshno.tech <token>
//
// Сценарий: положить токен в localStorage, открыть каждый раздел, проверить
// ожидаемые элементы, затем отправить запрос в чат и дождаться ответа модели.
// Результат — JSON с отчётом по шагам; скриншоты не нужны.

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

const [, , baseUrl, token, email, password] = process.argv;
if (!baseUrl || (!token && !(email && password))) {
  console.error("usage: node tools/browser_check.mjs <base-url> <token> | <base-url> - <email> <password>");
  process.exit(2);
}

const chromePath = CHROME_CANDIDATES.find((path) => existsSync(path));
if (!chromePath) {
  console.error("Chrome не найден: укажите CHROME_PATH");
  process.exit(2);
}

const PORT = 9334;
const profile = join(tmpdir(), `cn-check-${Date.now()}`);
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

async function evaluate(expression) {
  const result = await session.send(
    "Runtime.evaluate",
    { expression, returnByValue: true, awaitPromise: true },
    sessionId,
  );
  if (result.exceptionDetails) {
    throw new Error(result.exceptionDetails.text + " " + (result.exceptionDetails.exception?.description ?? ""));
  }
  return result?.result?.value;
}

async function goto(url, waitMs) {
  await session.send("Page.navigate", { url }, sessionId);
  await sleep(waitMs);
}

const checks = [];
const record = (name, ok, detail) => checks.push({ name, ok, detail });

// --- вход в панель ---
// Без сохранённого токена панель обязана показать форму входа. Проверяем это
// на чистом профиле: иначе гард мог бы пропустить всех подряд и тест бы врёт.
await goto(baseUrl, 1800);
const loginForm = await evaluate(`(() => {
  const card = document.querySelector('.login-card');
  if (!card) return null;
  return {
    hasEmail: Boolean(card.querySelector('input[type="email"]')),
    hasPassword: Boolean(card.querySelector('input[type="password"]')),
    text: card.innerText,
  };
})()`);
record("вход: форма показана без токена", Boolean(loginForm?.hasEmail && loginForm?.hasPassword),
  loginForm?.text?.slice(0, 80).replace(/\s+/g, " ") ?? "формы нет");

if (email && password) {
  await evaluate(`
    (() => {
      const set = (selector, value) => {
        const input = document.querySelector(selector);
        const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
        setter.call(input, value);
        input.dispatchEvent(new Event('input', { bubbles: true }));
      };
      set('.login-card input[type="email"]', ${JSON.stringify(email)});
      set('.login-card input[type="password"]', ${JSON.stringify(password)});
      return true;
    })()
  `);
  await sleep(200);
  await evaluate(`
    (() => {
      const button = document.querySelector('.login-card button[type="submit"]');
      if (!button) return false;
      button.click();
      return true;
    })()
  `);

  let signedIn = false;
  for (let attempt = 0; attempt < 40; attempt += 1) {
    await sleep(500);
    signedIn = await evaluate("Boolean(document.querySelector('.shell'))");
    if (signedIn) break;
  }
  const loginError = await evaluate("document.querySelector('.login-card .error-text')?.innerText ?? ''");
  record("вход: пароль принят", signedIn, signedIn ? "" : loginError.slice(0, 120));
  if (!signedIn) {
    console.log(JSON.stringify({ checks }, null, 2));
    chrome.kill();
    process.exit(1);
  }
} else {
  await evaluate(`localStorage.setItem("cn.admin_token", ${JSON.stringify(token)})`);
}

await goto(`${baseUrl}/#nodes`, 2000);
const nodeText = await evaluate("document.body.innerText");
record("узлы: список загрузился", nodeText.includes("owner-pc"), nodeText.match(/owner-pc[^\n]*/)?.[0] ?? "нет строки owner-pc");
record("узлы: состояние узла видно", /Доступен|Пауза/.test(nodeText), "");
record("узлы: реальная телеметрия", /°C|ток\/с|GB/.test(nodeText), "");
record("узлы: заявленная модель", nodeText.includes("qwen3:8b"), "");
record("узлы: окно контекста показано", /токенов/.test(nodeText), "");
record("узлы: веса проверены по манифесту", /подтверждены|манифест не задан|не сходятся/.test(nodeText), nodeText.match(/(подтверждены|манифест не задан|не сходятся)/)?.[0] ?? "");
record("узлы: модель эмбеддингов видна", /эмбеддинги:/.test(nodeText), nodeText.match(/эмбеддинги: *\S+/)?.[0] ?? "");
record("узлы: класс доверия управляется", /своя|проверенный|публичный/.test(nodeText), nodeText.match(/(своя|проверенный|публичный)/)?.[0] ?? "");
record("узлы: подключение новой машины", nodeText.includes("Подключить компьютер"), "");
record("узлы: выпуск приглашения", nodeText.includes("Выпустить приглашение"), "");

await goto(`${baseUrl}/#models`, 2000);
const modelsText = await evaluate("document.body.innerText");
record("модели: сервемая модель видна", modelsText.includes("qwen3:8b"), "");
record("модели: указан узел", modelsText.includes("owner-pc"), "");
record("модели: каталог ревизий есть", /Каталог ревизий/.test(modelsText), "");
record("модели: форма ревизии и файлов", modelsText.includes("Новая ревизия") && modelsText.includes("Файлы ревизии"), "");
record("модели: состояние манифеста", /подписан|не подписан/.test(modelsText), modelsText.match(/(не подписан|подписан)/)?.[0] ?? "");
record("модели: кнопка подписи", modelsText.includes("Подписать"), "");

// --- знания: страница, индексация документа и поиск по смыслу ---
await goto(`${baseUrl}/#knowledge`, 2000);
const knowledgeText = await evaluate("document.body.innerText");
record("знания: страница открылась", knowledgeText.includes("Знания") && knowledgeText.includes("База знаний"), "");
record("знания: форма документа", knowledgeText.includes("Добавить документ"), "");

const knowledgeDoc = `Правило проверки ${Date.now()}: узел без heartbeat дольше трёх минут считается offline, а его задания возвращаются в очередь.`;
await evaluate(`
  (() => {
    const set = (id, value, proto) => {
      const input = document.getElementById(id);
      if (!input) return false;
      const setter = Object.getOwnPropertyDescriptor(proto.prototype, 'value').set;
      setter.call(input, value);
      input.dispatchEvent(new Event('input', { bubbles: true }));
      return true;
    };
    set('knowledge-title', 'Проверка панели', HTMLInputElement);
    set('knowledge-text', ${JSON.stringify(knowledgeDoc)}, HTMLTextAreaElement);
    return true;
  })()
`);
await sleep(300);
await evaluate(`
  (() => {
    const button = [...document.querySelectorAll('button')].find((el) => el.textContent?.includes('Проиндексировать'));
    button?.click();
    return true;
  })()
`);
let indexed = false;
for (let attempt = 0; attempt < 60; attempt += 1) {
  await sleep(1000);
  indexed = await evaluate("document.body.innerText.includes('проиндексирован')");
  if (indexed) break;
}
record("знания: документ проиндексирован", indexed, indexed ? "" : "нет подтверждения за минуту");

if (indexed) {
  await evaluate(`
    (() => {
      const input = document.getElementById('knowledge-query');
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
      setter.call(input, 'сколько времени узел без heartbeat считается офлайн');
      input.dispatchEvent(new Event('input', { bubbles: true }));
      const button = [...document.querySelectorAll('button')].find((el) => el.textContent?.trim() === 'Найти');
      button?.click();
      return true;
    })()
  `);
  let found = "";
  for (let attempt = 0; attempt < 30; attempt += 1) {
    await sleep(1000);
    found = await evaluate("document.body.innerText.match(/близость [0-9.]+/) ? document.body.innerText : ''");
    if (found) break;
  }
  record("знания: поиск нашёл фрагмент", Boolean(found) && found.includes("Правило проверки"), "поиск по смыслу");
}

await goto(`${baseUrl}/#topology`, 2000);
const topologyText = await evaluate("document.body.innerText");
record("топология: страница открылась", topologyText.includes("Топология") && topologyText.includes("слоям"), "");
record("топология: честное пустое состояние", /Размещений пока нет|Версии размещения/.test(topologyText), "");

await goto(`${baseUrl}/#jobs`, 2000);
const jobsText = await evaluate("document.body.innerText");
record("задания: журнал не пуст", /Выполнено[^\n]*\n?\s*[1-9]/.test(jobsText) || jobsText.includes("Готово"), "");
record("задания: скорость показана", /tok\/s|—/.test(jobsText), "");
record("задания: доказательство модели", jobsText.includes("Модель узла"), "");
record("задания: digest узла виден", /не сообщено|…/.test(jobsText), "");
record("задания: статус весов", /подтверждены|не сверялись/.test(jobsText), jobsText.match(/(подтверждены|не сверялись)/)?.[0] ?? "");

await goto(`${baseUrl}/#settings`, 1800);
const settingsText = await evaluate("document.body.innerText");
record("настройки: аккаунт показан", settingsText.includes("Аккаунт") && settingsText.includes("@"), "");
record("настройки: смена пароля", settingsText.includes("Сменить пароль"), "");
record("настройки: активные входы", settingsText.includes("Активные входы"), "");
const hasSessionTable = await evaluate("Boolean(document.querySelector('#settings ~ * table, table'))");
record("настройки: таблица сессий отрисована", hasSessionTable === true, "");

// --- доступ к панели: создание и закрытие аккаунта прямо из интерфейса ---
const probeEmail = `check-${Date.now()}@nshno.tech`;
await evaluate(`
  (() => {
    const set = (id, value, proto = HTMLInputElement) => {
      const input = document.getElementById(id);
      if (!input) return false;
      const setter = Object.getOwnPropertyDescriptor(proto.prototype, 'value').set;
      setter.call(input, value);
      input.dispatchEvent(new Event('input', { bubbles: true }));
      return true;
    };
    set('account-email', ${JSON.stringify(probeEmail)});
    set('account-name', 'Проверка панели');
    set('account-password', 'Check-Access-2211');
    return true;
  })()
`);
await sleep(300);
await evaluate(`
  (() => {
    const button = [...document.querySelectorAll('button')].find((el) => el.textContent?.trim() === 'Создать');
    button?.click();
    return true;
  })()
`);
await sleep(2500);
const afterCreate = await evaluate("document.body.innerText");
record("доступ: аккаунт создан из панели", afterCreate.includes(probeEmail), probeEmail);

// Закрываем доступ у созданного аккаунта (кнопка в его строке).
// Подтверждение подменяем сразу: в headless-браузере модальное окно остановило
// бы выполнение скрипта.
await evaluate("window.confirm = () => true");
await evaluate(`
  (() => {
    const row = [...document.querySelectorAll('tr')].find((el) => el.textContent?.includes(${JSON.stringify(probeEmail)}));
    if (!row) return false;
    const button = [...row.querySelectorAll('button')].find((el) => el.textContent?.includes('Закрыть доступ'));
    button?.click();
    return true;
  })()
`);
await sleep(2500);
// Проверяем именно строку таблицы: адрес мог остаться в тексте уведомления
// «Готово: …», и по всему тексту страницы судить нельзя.
const rowsLeft = await evaluate(`
  [...document.querySelectorAll('tr')]
    .filter((row) => row.textContent?.includes(${JSON.stringify(probeEmail)})).length
`);
record("доступ: аккаунт закрыт из панели", rowsLeft === 0, `строк с адресом: ${rowsLeft}`);

// --- чат целиком: ввод, отправка, ответ модели ---
await goto(`${baseUrl}/#chat`, 2500);
const modelCount = await evaluate("document.querySelectorAll('.chat-toolbar .select-field select option').length");
record("чат: список моделей", modelCount > 0, `вариантов: ${modelCount}`);

if (modelCount > 0) {
  await evaluate(`
    (() => {
      const area = document.querySelector('.composer-input');
      const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set;
      setter.call(area, 'Ответь одним коротким предложением: зачем нужен heartbeat?');
      area.dispatchEvent(new Event('input', { bubbles: true }));
      return true;
    })()
  `);
  await sleep(300);

  const clicked = await evaluate(`
    (() => {
      const button = [...document.querySelectorAll('.composer-actions button')]
        .find((el) => el.type === 'submit');
      if (!button) return false;
      button.click();
      return true;
    })()
  `);
  record("чат: отправка нажата", clicked === true, "");

  let answer = "";
  let meta = "";
  for (let attempt = 0; attempt < 90; attempt += 1) {
    await sleep(1000);
    answer = await evaluate(`(() => {
      const nodes = [...document.querySelectorAll('.turn[data-role="assistant"] .turn-body')];
      return nodes.length ? nodes[nodes.length - 1].innerText : "";
    })()`);
    meta = await evaluate(`(() => {
      const nodes = [...document.querySelectorAll('.turn[data-role="assistant"] .turn-meta')];
      return nodes.length ? nodes[nodes.length - 1].innerText : "";
    })()`);
    const streaming = await evaluate("Boolean(document.querySelector('.caret'))");
    if (answer && !streaming && meta) break;
    if (answer && !streaming && attempt > 3) break;
  }

  record("чат: ответ модели получен", Boolean(answer && answer.length > 5), answer.slice(0, 120).replace(/\s+/g, " "));
  record("чат: счётчики токенов пришли", /токенов|tok\/s/.test(meta), meta.replace(/\s+/g, " "));

  // --- история на сервере: диалог попал в список и переживает перезагрузку ---
  await sleep(1500);
  const historyList = await evaluate(`
    (() => {
      const items = [...document.querySelectorAll('.history-item')];
      const active = document.querySelector('.history-item[data-active="true"]');
      return { total: items.length, hasActive: Boolean(active) };
    })()
  `);
  record("чат: диалог сохранён в истории", historyList.total > 0 && historyList.hasActive,
    `${historyList.total} в списке`);

  // Перезагрузка страницы: разговор должен восстановиться с сервера.
  await goto(`${baseUrl}/#chat`, 3000);
  const restored = await evaluate(`
    (() => {
      const nodes = [...document.querySelectorAll('.turn[data-role="assistant"] .turn-body')];
      const active = document.querySelector('.history-item[data-active="true"]');
      return {
        answer: nodes.length ? nodes[nodes.length - 1].innerText : "",
        hasActive: Boolean(active),
      };
    })()
  `);
  record("чат: история восстановлена после перезагрузки",
    Boolean(restored.answer && restored.answer.length > 5) && restored.hasActive,
    restored.answer.slice(0, 80).replace(/\s+/g, " "));
}

console.log(JSON.stringify({ checks }, null, 2));
chrome.kill();
process.exit(checks.every((check) => check.ok) ? 0 : 1);
