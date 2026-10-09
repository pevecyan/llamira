// Playground chat history, kept in the browser (IndexedDB): per device, survives reloads.
// Large attached images are not stored (replaced by a placeholder) to keep the database small.

import type { Sampling } from './chatstream'

export interface StoredMsg {
  role: 'user' | 'assistant' | 'system'
  content: string
  reasoning?: string
  images?: string[]
  imagesDropped?: number
  timings?: { prompt_n: number; prompt_per_second: number; predicted_n: number; predicted_per_second: number }
  error?: string
  finish?: string
}

export interface Chat {
  id: string
  host: string
  title: string
  model: string
  system: string
  temperature: number
  maxTokens: number
  sampling?: Sampling
  messages: StoredMsg[]
  created: number
  updated: number
}

const DB = 'llamira'
const STORE = 'chats'
const LIMIT_KEY = 'llamira.chatLimit'
export const MAX_IMAGE_BYTES = 300 * 1024 // per image (data URL length)

let dbp: Promise<IDBDatabase> | null = null
function db(): Promise<IDBDatabase> {
  if (!dbp) {
    dbp = new Promise((resolve, reject) => {
      const req = indexedDB.open(DB, 1)
      req.onupgradeneeded = () => {
        const s = req.result.createObjectStore(STORE, { keyPath: 'id' })
        s.createIndex('host', 'host')
      }
      req.onsuccess = () => resolve(req.result)
      req.onerror = () => reject(req.error)
    })
  }
  return dbp
}

function tx<T>(mode: IDBTransactionMode, fn: (s: IDBObjectStore) => IDBRequest<T> | void): Promise<T | undefined> {
  return db().then(
    (d) =>
      new Promise((resolve, reject) => {
        const t = d.transaction(STORE, mode)
        const r = fn(t.objectStore(STORE))
        t.oncomplete = () => resolve(r ? (r as IDBRequest<T>).result : undefined)
        t.onerror = () => reject(t.error)
      }),
  )
}

export function chatLimit(): number {
  try {
    const n = parseInt(localStorage.getItem(LIMIT_KEY) ?? '', 10)
    return n > 0 ? n : 20
  } catch {
    return 20
  }
}

export function setChatLimit(n: number) {
  try {
    localStorage.setItem(LIMIT_KEY, String(Math.max(1, Math.min(500, Math.round(n)))))
  } catch {
    /* ignore */
  }
}

export async function listChats(host: string): Promise<Chat[]> {
  try {
    const all = ((await tx<Chat[]>('readonly', (s) => s.index('host').getAll(host))) ?? []) as Chat[]
    return all.sort((a, b) => b.updated - a.updated)
  } catch {
    return []
  }
}

// saveChat stores a chat (images over the cap are dropped) and trims the history to the limit.
export async function saveChat(c: Chat): Promise<void> {
  const copy: Chat = JSON.parse(JSON.stringify(c))
  for (const m of copy.messages) {
    if (!m.images) continue
    const kept = m.images.filter((u) => u.length <= MAX_IMAGE_BYTES)
    m.imagesDropped = (m.imagesDropped ?? 0) + (m.images.length - kept.length)
    m.images = kept.length ? kept : undefined
  }
  try {
    await tx('readwrite', (s) => s.put(copy))
    const all = await listChats(c.host)
    for (const old of all.slice(chatLimit())) await deleteChat(old.id)
  } catch {
    /* storage unavailable (private mode): history just isn't kept */
  }
}

export async function deleteChat(id: string): Promise<void> {
  try {
    await tx('readwrite', (s) => s.delete(id))
  } catch {
    /* ignore */
  }
}

export function newChatId(): string {
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`
}

export function titleFrom(messages: StoredMsg[]): string {
  const first = messages.find((m) => m.role === 'user')?.content.trim() ?? ''
  const t = first.replace(/\s+/g, ' ')
  return t ? (t.length > 60 ? t.slice(0, 57) + '…' : t) : 'New chat'
}
