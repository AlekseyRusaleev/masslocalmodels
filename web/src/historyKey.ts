/** Ключ истории живёт только в этом браузере. На сервер уходит шифротекст. */

const KEY_STORAGE = "cn.history_key";
const ID_STORAGE = "cn.history_key_id";
const ACK_STORAGE = "cn.history_key_ack";

export interface SealedBlob {
  key_id: string;
  nonce: string;
  ciphertext: string;
}

export interface HistoryKeyStatus {
  keyId: string;
  phrase: string;
  needsAck: boolean;
}

function bytesToB64(bytes: Uint8Array): string {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

function b64ToBytes(value: string): Uint8Array<ArrayBuffer> {
  const binary = atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index);
  return bytes;
}

export function ensureHistoryKey(): HistoryKeyStatus {
  let raw = localStorage.getItem(KEY_STORAGE);
  let keyId = localStorage.getItem(ID_STORAGE);
  if (!raw || !keyId) {
    const bytes = crypto.getRandomValues(new Uint8Array(32));
    raw = bytesToB64(bytes);
    keyId = crypto.randomUUID();
    localStorage.setItem(KEY_STORAGE, raw);
    localStorage.setItem(ID_STORAGE, keyId);
    localStorage.removeItem(ACK_STORAGE);
  }
  return {
    keyId,
    phrase: `${keyId}.${raw}`,
    needsAck: localStorage.getItem(ACK_STORAGE) !== "1",
  };
}

export function acknowledgeHistoryKey(): void {
  localStorage.setItem(ACK_STORAGE, "1");
}

export function importHistoryPhrase(phrase: string): void {
  const [keyId, raw] = phrase.trim().split(".");
  if (!keyId || !raw) throw new Error("фраза должна быть вида идентификатор.ключ");
  const bytes = b64ToBytes(raw);
  if (bytes.length !== 32) throw new Error("ключ должен быть 32 байта");
  localStorage.setItem(ID_STORAGE, keyId);
  localStorage.setItem(KEY_STORAGE, bytesToB64(bytes));
  localStorage.setItem(ACK_STORAGE, "1");
}

async function cryptoKey(): Promise<{ id: string; key: CryptoKey }> {
  const status = ensureHistoryKey();
  const raw = localStorage.getItem(KEY_STORAGE);
  if (!raw) throw new Error("ключа истории нет");
  const key = await crypto.subtle.importKey("raw", b64ToBytes(raw), "AES-GCM", false, ["encrypt", "decrypt"]);
  return { id: status.keyId, key };
}

export async function sealText(plain: string): Promise<SealedBlob> {
  const { id, key } = await cryptoKey();
  const nonce = crypto.getRandomValues(new Uint8Array(12));
  const cipher = await crypto.subtle.encrypt(
    { name: "AES-GCM", iv: nonce },
    key,
    new TextEncoder().encode(plain),
  );
  return { key_id: id, nonce: bytesToB64(nonce), ciphertext: bytesToB64(new Uint8Array(cipher)) };
}

export async function openText(blob: SealedBlob): Promise<string> {
  const { id, key } = await cryptoKey();
  if (blob.key_id !== id) {
    throw new Error("этот диалог зашифрован другим ключом");
  }
  const plain = await crypto.subtle.decrypt(
    { name: "AES-GCM", iv: b64ToBytes(blob.nonce) },
    key,
    b64ToBytes(blob.ciphertext),
  );
  return new TextDecoder().decode(plain);
}

export async function sealPair(content: string, reasoning: string): Promise<SealedBlob> {
  return sealText(JSON.stringify({ content, reasoning }));
}

export async function openPair(blob: SealedBlob): Promise<{ content: string; reasoning: string }> {
  const text = await openText(blob);
  const parsed = JSON.parse(text) as { content?: string; reasoning?: string };
  return { content: parsed.content ?? "", reasoning: parsed.reasoning ?? "" };
}
