// Проверяет именно тот сценарий, на который пожаловался владелец: после
// «Нового» старый диалог должен остаться видимым в списке и открываться.
//
//   node tools/chat_history_check.mjs <base-url> <email> <password>
import { startBrowser } from "./cdp.mjs";

const [, , baseUrl, email, password] = process.argv;
if (!baseUrl || !email || !password) {
  console.error("usage: node tools/chat_history_check.mjs <base-url> <email> <password>");
  process.exit(2);
}

const browser = await startBrowser({ port: 9343, prefix: "cn-history" });
const { evaluate, goto, sleep } = browser;

const checks = [];
const record = (name, ok, detail = "") => checks.push({ name, ok, detail });

await goto(baseUrl, 2500);
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
await sleep(3500);

/** Задаёт вопрос и ждёт конца генерации. */
async function ask(text) {
  await evaluate(`
    (() => {
      const area = document.querySelector('.composer-input');
      const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set;
      setter.call(area, ${JSON.stringify(text)});
      area.dispatchEvent(new Event('input', { bubbles: true }));
      return true;
    })()
  `);
  await sleep(200);
  await evaluate(`
    (() => {
      const button = [...document.querySelectorAll('.composer-actions button')].find((el) => el.type === 'submit');
      button?.click();
      return true;
    })()
  `);
  for (let attempt = 0; attempt < 90; attempt += 1) {
    await sleep(1000);
    const done = await evaluate(`
      (() => {
        const caret = Boolean(document.querySelector('.caret'));
        const nodes = [...document.querySelectorAll('.turn[data-role="assistant"] .turn-body')];
        return !caret && nodes.length > 0 && (nodes[nodes.length - 1].innerText ?? "").length > 3;
      })()
    `);
    if (done) return true;
  }
  return false;
}

// 1. Первый разговор.
await goto(`${baseUrl}/#chat`, 3000);
record("первый ход сохранён", await ask("Первое сообщение: назови одну причину хранить историю диалогов."));

const afterFirst = await evaluate(`
  (() => {
    const items = [...document.querySelectorAll('.history-item')];
    return { count: items.length };
  })()
`);
record("первый диалог виден в списке", afterFirst.count >= 1, `диалогов: ${afterFirst.count}`);

// 2. «Новый» — старый должен остаться в списке, поле чата — пустым.
await evaluate(`
  (() => {
    const button = [...document.querySelectorAll('.chat-history-head button')][0];
    button?.click();
    return true;
  })()
`);
await sleep(900);
const afterNew = await evaluate(`
  (() => {
    const items = [...document.querySelectorAll('.history-item')];
    return { count: items.length, turns: document.querySelectorAll('.turn').length };
  })()
`);
record("после «Нового» старый диалог остался в списке", afterNew.count >= 1, `диалогов: ${afterNew.count}`);
record("после «Нового» поле чата пустое", afterNew.turns === 0, `ходов: ${afterNew.turns}`);

// 3. Второй разговор — в списке их уже два.
record("второй ход сохранён", await ask("Второе сообщение: чем отличается пауза узла от его остановки?"));
await sleep(1500);
const afterSecond = await evaluate(`
  (() => {
    const items = [...document.querySelectorAll('.history-item')];
    return {
      count: items.length,
      titles: items.map((item) => item.querySelector('.history-title')?.textContent ?? ""),
    };
  })()
`);
record("оба диалога в списке", afterSecond.count >= 2, `диалогов: ${afterSecond.count}`);

// 4. Клик по первому разговору возвращает его содержимое.
await evaluate(`
  (() => {
    const items = [...document.querySelectorAll('.history-item')];
    const last = items[items.length - 1];
    last?.querySelector('.history-open')?.click();
    return true;
  })()
`);
await sleep(2500);
const opened = await evaluate(`
  (() => {
    const nodes = [...document.querySelectorAll('.turn[data-role="user"] .turn-body')];
    const active = document.querySelector('.history-item[data-active="true"] .history-title')?.textContent ?? "";
    return { first: nodes[0]?.innerText ?? "", active, count: nodes.length };
  })()
`);
record("старый диалог открывается по клику", opened.first.includes("Первое сообщение"), opened.first.slice(0, 60));
record("выделен именно открытый диалог", opened.active !== "", `активный: ${opened.active}`);

// 5. Удаление диалога из списка.
const beforeDelete = afterSecond.count;
await evaluate(`
  (() => {
    const items = [...document.querySelectorAll('.history-item')];
    items[items.length - 1]?.querySelector('.history-delete')?.click();
    return true;
  })()
`);
await sleep(1500);
const afterDelete = await evaluate(`document.querySelectorAll('.history-item').length`);
record("диалог удаляется из списка", afterDelete === beforeDelete - 1, `${beforeDelete} → ${afterDelete}`);

console.table(checks);
browser.close();
process.exit(checks.every((check) => check.ok) ? 0 : 1);
