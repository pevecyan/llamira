import { computed, reactive } from 'vue'
import {
  hostApi, listHosts, logout, onLoginRequired, session, type GPU, type HostApi, type HostInfo, type Info, type LocalModel, type LocalResp, type Model, type Status,
  type HostStats, type UpdateInfo,
} from './api'

export interface LoadProgress {
  stage?: string
  value?: number
}

// Global, per-selected-host state fed by REST calls and the /events stream.
export const state = reactive({
  hosts: [] as HostInfo[],
  host: '' as string,
  info: null as Info | null,
  status: null as Status | null,
  models: [] as Model[],
  local: null as LocalResp | null,
  update: null as UpdateInfo | null, // newer llama.cpp release (checked by the manager every 6 h)
  gpus: [] as GPU[],
  gpuHistory: {} as Record<number, number[]>, // utilisation samples for sparklines
  hostStats: null as HostStats | null, // RAM / CPU of the selected host
  cpuHistory: [] as number[], // host CPU % samples for the sparkline
  progress: {} as Record<string, LoadProgress>,
  routerConnected: false,
  eventsConnected: false,
  error: '' as string,
  busy: {} as Record<string, boolean>,
  // preset text handed to the Presets editor (restore from history/backup)
  pending: null as { text: string; label: string } | null,
  loginRequired: false, // the server wants the password (App shows the login form)
  canLogout: false, // logged in with the password
})

export const api = computed<HostApi | null>(() => (state.host ? hostApi(state.host) : null))

let events: EventSource | null = null
let statusTimer: number | undefined
let modelsTimer: number | undefined

const HOST_KEY = 'llamira.host'

onLoginRequired(() => {
  state.loginRequired = true
  events?.close()
  window.clearInterval(statusTimer)
})

export async function initHosts() {
  try {
    state.hosts = await listHosts()
  } catch (e: any) {
    state.error = e.message
    return
  }
  session()
    .then((s) => (state.canLogout = s?.logout === true))
    .catch(() => {})
  let saved = ''
  try {
    saved = localStorage.getItem(HOST_KEY) ?? ''
  } catch {
    /* storage unavailable */
  }
  const pick = state.hosts.find((h) => h.name === saved) ?? state.hosts.find((h) => h.connected) ?? state.hosts[0]
  if (pick) await selectHost(pick.name)
}

// Ends the password session and reloads into the login form.
export async function signOut() {
  try {
    await logout()
  } finally {
    window.location.reload()
  }
}

export async function selectHost(name: string) {
  events?.close()
  window.clearInterval(statusTimer)
  Object.assign(state, {
    host: name, info: null, status: null, models: [], local: null, update: null, gpus: [], gpuHistory: {}, hostStats: null, cpuHistory: [], progress: {}, error: '',
    routerConnected: false, eventsConnected: false,
  })
  try {
    localStorage.setItem(HOST_KEY, name)
  } catch {
    /* ignore */
  }
  const a = hostApi(name)
  try {
    state.info = await a.info()
  } catch (e: any) {
    state.error = e.message
    return
  }
  await Promise.all([refreshStatus(), refreshModels()])
  statusTimer = window.setInterval(refreshStatus, 10_000)
  subscribe(a)
  refreshUpdate()
}

export async function refreshUpdate() {
  if (!api.value) return
  try {
    state.update = await api.value.llamacppUpdate()
  } catch {
    /* optional */
  }
}

export async function refreshStatus() {
  if (!api.value) return
  try {
    const s = await api.value.status()
    state.status = s
    if (s.gpus) pushGpus(s.gpus)
    if (s.host) pushHost(s.host)
  } catch (e: any) {
    state.error = e.message
  }
}

export async function refreshModels() {
  if (!api.value) return
  try {
    state.models = await api.value.models()
    state.error = ''
  } catch (e: any) {
    state.error = e.message
  }
  refreshLocal()
}

export async function refreshLocal() {
  if (!api.value) return
  try {
    state.local = await api.value.local()
  } catch {
    /* optional information */
  }
}

export function localOf(id: string): LocalModel | undefined {
  return state.local?.models.find((m) => m.id === id)
}

// Download progress of a model (sum over its files), 0..1, or null when not downloading.
export function downloadProgress(m: Model): { done: number; total: number; pct: number } | null {
  if (m.status.value !== 'downloading') return null
  let done = 0
  let total = 0
  for (const p of Object.values(m.status.progress ?? {})) {
    done += p.done
    total += p.total
  }
  return { done, total, pct: total ? done / total : 0 }
}

function refreshModelsSoon() {
  window.clearTimeout(modelsTimer)
  modelsTimer = window.setTimeout(refreshModels, 300)
}

function pushGpus(gpus: GPU[]) {
  state.gpus = gpus
  for (const g of gpus) {
    const h = (state.gpuHistory[g.index] ??= [])
    h.push(g.util_pct)
    if (h.length > 60) h.shift()
  }
}

function pushHost(h: HostStats) {
  if (state.hostStats?.time === h.time) return
  state.hostStats = h
  state.cpuHistory.push(h.cpu_pct)
  if (state.cpuHistory.length > 60) state.cpuHistory.shift()
}

function subscribe(a: HostApi) {
  events = a.events()
  events.onopen = () => (state.eventsConnected = true)
  events.onerror = () => (state.eventsConnected = false)
  events.addEventListener('gpu', (e) => pushGpus(JSON.parse((e as MessageEvent).data)))
  events.addEventListener('host', (e) => pushHost(JSON.parse((e as MessageEvent).data)))
  events.addEventListener('router', (e) => {
    const d = JSON.parse((e as MessageEvent).data)
    state.routerConnected = d.connected
    if (d.connected) refreshModelsSoon()
  })
  events.addEventListener('model', (e) => {
    const ev = JSON.parse((e as MessageEvent).data) as { model: string; event: string; data?: any }
    const m = state.models.find((x) => x.id === ev.model)
    const st = ev.data?.status
    if (m && typeof st === 'string') {
      m.status.value = st
      if (st === 'loading' && ev.data.progress) {
        state.progress[ev.model] = { stage: ev.data.progress.current, value: ev.data.progress.value }
      }
      if (st !== 'loading') delete state.progress[ev.model]
    }
    if (ev.event === 'download_progress') {
      // payload: {"progress": {"<url>": {"done": n, "total": n}}} (GET /models has no progress)
      if (m) {
        m.status.value = 'downloading'
        m.status.progress = ev.data?.progress ?? ev.data
      } else refreshModelsSoon()
      return // frequent: no full refresh
    }
    if (st !== 'loading') refreshModelsSoon()
  })
}

export function isRunning(m: Model) {
  return ['loaded', 'loading', 'sleeping'].includes(m.status.value)
}

// Parses "key = value" lines of a router-reported effective preset.
export function presetValues(preset?: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const line of (preset ?? '').split('\n')) {
    const i = line.indexOf('=')
    if (i > 0 && !line.startsWith('[')) out[line.slice(0, i).trim()] = line.slice(i + 1).trim()
  }
  return out
}

export function fmtBytes(n?: number) {
  if (!n) return '–'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  while (n >= 1024 && i < u.length - 1) {
    n /= 1024
    i++
  }
  return `${n.toFixed(i >= 3 ? 1 : 0)} ${u[i]}`
}

export function fmtCtx(v?: string | number) {
  const n = Number(v)
  if (!v || Number.isNaN(n)) return v ? String(v) : '–'
  return n >= 1024 && n % 1024 === 0 ? `${n / 1024}K` : String(n)
}
