// Typed client for the Llamira API. Every call is scoped to a host:
// /api/hosts/{host}/... (served directly by `serve` as host "local", or proxied over SSH by `connect`).

export interface ModelStatus {
  value: 'unloaded' | 'loading' | 'loaded' | 'sleeping' | 'downloading' | string
  args?: string[]
  preset?: string
  failed?: boolean
  exit_code?: number
  progress?: Record<string, { done: number; total: number }>
}

export interface Model {
  id: string
  aliases: string[]
  tags: string[]
  status: ModelStatus
  source: 'cache' | 'dir' | 'preset' | string
  can_remove: boolean
  architecture?: { input_modalities: string[]; output_modalities: string[] }
  meta?: { n_ctx?: number; n_ctx_train?: number; n_params?: number; size?: number; ftype?: string }
}

export interface GPU {
  index: number
  vendor?: 'nvidia' | 'amd'
  name: string
  uuid: string
  mem_total_mib: number
  mem_used_mib: number
  util_pct: number
  temp_c: number
  power_w: number
  power_limit_w: number
}

export interface HostStats {
  time: string
  mem_total: number
  mem_used: number
  mem_available: number
  swap_total: number
  swap_used: number
  cores: number
  cpu_pct: number
  per_core: number[] | null
  load: [number, number, number]
  cpu_temp_c?: number
  // the llama-server unit (router + model processes): cgroup, or summed process RSS
  unit?: { name: string; source: 'cgroup' | 'proc'; mem: number; anon: number; file: number; cpu_pct: number; pids?: number }
}

export interface Status {
  router: { healthy: boolean; error?: string; props?: { build_info?: string; max_instances?: number; models_autoload?: boolean } }
  service: { unit: string; active_state: string; sub_state: string; main_pid: string; since: string; exec_start: string; drop_ins: string }
  gpus: GPU[] | null
  devices?: string[] // llama.cpp device name per GPU (CUDA0, ROCm0, Vulkan0, …)
  host?: HostStats
  gpu_error?: string
  time: string
}

export interface HostInfo {
  name: string
  label: string
  connected: boolean
  error?: string
}

export interface Info {
  version: string
  hostname: string
  preset_file: string
  router_url: string
  service: string
  models_dir: string
  read_only?: boolean // signed in with the read-only token: changes are refused
}

export interface Arg {
  key: string
  names: string[]
  neg_names: string[] | null
  env?: string
  value?: string
  allowed?: string[]
  description: string
  group: string
}

export interface ArgCatalog {
  version: string
  args: Arg[]
  preset_only: Record<string, string>
  router_owned: Record<string, boolean>
}

export interface KV {
  key: string
  value: string
}

export interface Section {
  name: string
  entries: KV[]
  comments: string[]
}

export interface PresetDoc {
  text: string
  preamble: KV[]
  sections: Section[]
}

export interface PresetFile extends PresetDoc {
  path: string
  hash: string
  backups: string[]
}

export interface DiffLine {
  op: ' ' | '+' | '-'
  text: string
}

export interface Issue {
  severity: 'error' | 'warning'
  section?: string
  key?: string
  message: string
}

export interface ModelChange {
  id: string
  change: 'added' | 'removed' | 'changed' | 'aliases'
  status: string
  unloads: boolean
  old_preset?: string
  new_preset?: string
}

export interface Preview {
  text: string
  base_hash: string
  diff: DiffLine[]
  changed: boolean
  issues: Issue[]
  dry_run_ok: boolean
  dry_run_error?: string
  dry_run_log?: string
  models: ModelChange[] | null
}

export interface Step {
  name: string
  ok: boolean
  detail?: string
  duration_ms: number
}

export interface ApplyResult {
  ok: boolean
  error?: string
  steps: Step[]
  backup?: string
  rolled_back: boolean
  models: ModelChange[] | null
  apply_id: number
}

export interface ApplyRecord {
  id: number
  time: string
  ok: boolean
  summary: string
  error?: string
  steps: Step[]
  backup?: string
  before?: string
  after?: string
  diff?: DiffLine[]
}

export interface HFRepo {
  id: string
  downloads: number
  likes: number
  pipeline_tag?: string
  created_at: string
  tags: string[]
}

export interface HFFile {
  path: string
  size: number
  sha256?: string
}

export interface Quant {
  tag: string
  ref: string
  files: HFFile[]
  size: number
  extra_size: number
  extras: string[]
  bits: number
  ambiguous: boolean
  selectable: boolean
  dir?: string
}

export interface HFDetail {
  id: string
  author: string
  downloads: number
  likes: number
  last_modified: string
  gated: boolean
  license?: string
  pipeline_tag?: string
  base_model?: string
  params?: number
  architecture?: string
  context_length?: number
  quants: Quant[]
  sidecars: HFFile[]
  url: string
}

export interface GGUFInfo {
  architecture: string
  name?: string
  size_label?: string
  file_type?: string
  context_length?: number
  block_count?: number
  embedding_length?: number
  head_count?: number
  head_count_kv?: number
  key_length?: number
  value_length?: number
  expert_count?: number
  expert_used_count?: number
  sliding_window?: number
  full_attention_interval?: number
}

export interface LocalModel {
  id: string
  repo: string
  tag: string
  files: { path: string; size: number }[]
  extras: string[]
  size: number
  modified: string
  gguf?: GGUFInfo
  error?: string
}

export interface Disk {
  path: string
  total: number
  free: number
  used: number
}

export interface DirFile {
  path: string
  size: number
  gguf?: GGUFInfo
}

export interface LocalResp {
  cache_dir: string
  models: LocalModel[]
  dir_files: DirFile[]
  preset_refs: Record<string, string[]>
  auto_presets?: string[] // downloads that get a preset when they finish
  disk_low_gb?: number // low-disk alert threshold (0 = off)
  disk?: Disk
  error?: string
}

export interface RecOption {
  tag: string
  ref: string
  size: number
  vram: number[] | null
  devices: string[] | null
  status: string
  placement: 'one-gpu' | 'all-gpus'
}

export interface Recommendation {
  name: string
  use: string[]
  repo: string
  ctx: number
  notes: string
  tier: 'one-gpu' | 'all-gpus' | 'low-quality' | 'offload' | 'unsupported' | 'error'
  pick?: RecOption
  smaller?: RecOption
  larger?: RecOption
  reason: string
  architecture?: string
  params?: number
  supported: boolean
  installed: string[]
  error?: string
}

export interface VramDevice {
  name: string
  layers: number
  weights: number
  mmproj: number
  kv: number
  overhead: number
  total: number
  capacity?: number
  status?: 'ok' | 'tight' | 'over'
}

export interface VramEstimate {
  architecture: string
  ctx: number
  ctx_train: number
  layers: number
  gpu_layers: number
  kv_layers: number
  swa_layers: number
  kv_per_token: number
  recurrent_layers: number
  slots: number
  devices: VramDevice[]
  cpu: VramDevice
  status: 'ok' | 'tight' | 'over'
  warnings: string[]
  notes: string[]
}

export interface VramFit {
  ctx: number
  capped: boolean
  estimate?: VramEstimate
  alt?: { cache_type: string; ctx: number }
}

export interface BenchGPU {
  index: number
  peak_mib: number
  max_temp_c: number
  avg_power_w: number
  max_power_w: number
  power_limit_w: number
}

export interface BenchResult {
  model: string
  preset: string
  build?: string // llama.cpp build of the router (b11036-911f6cdc8); missing in older results
  time: string
  load_ms: number
  prompt_n: number
  prompt_tps: number
  gen_n: number
  gen_tps: number
  ttft_ms: number
  total_ms: number
  gpus: BenchGPU[] | null
  error?: string
  restored?: string[]
  prompt_tokens?: number // requested sizes (newer results)
  gen_tokens?: number
  host?: { mem_total: number; peak_mem_used: number; peak_unit_mem?: number; peak_swap_used?: number; avg_cpu_pct: number; max_cpu_pct: number }
}

export interface LlamaBuild {
  ref: string
  dir: string
  binary: string
  commit?: string
  version?: string
  built_at: string
  ok: boolean
  error?: string
  dry_run?: string
  original?: boolean
  source?: string
  active?: boolean
  running?: boolean
}

export interface UpdateInfo {
  current: string
  current_version?: string // newest version at or below the running build
  builds_ahead?: number
  latest?: string // build tag of the newest version
  latest_version?: string // v0.6.0
  latest_build?: string // newest nightly build
  newer: boolean // the newest version is newer than the running build
  published?: string
  url?: string
  notes?: string
  checked: string
  error?: string
}

export interface LlamaRelease {
  tag: string // build tag (bNNNNN)
  version?: string // v0.6.0 for a version release
  published: string
  url: string
  notes?: string
}

export interface ModelUsage {
  model: string
  requests: number
  prompt_tokens: number
  gen_tokens: number
  prompt_tps: number
  gen_tps: number
  last_used: string
}

export interface UsageBucket {
  start: string
  model: string
  requests: number
  gen_tokens: number
  prompt_tokens: number
}

export interface CleanupCandidate {
  id: string
  size: number
  last_used?: string
  presets: string[]
  status: string
  suggest: boolean
}

export interface AlertSettings {
  webhook_url: string
  ntfy_url: string
  ntfy_token: string
  telegram_token: string
  telegram_chat: string
  router_down: boolean
  model_failed: boolean
  downloads: boolean
  schedule_failed: boolean
  new_release: boolean
  gpu_temp_c: number
  gpu_vram_pct: number
  ram_pct: number
  swap_mb: number
  disk_free_gb: number
  cooldown_min: number
}

export interface AlertEvent {
  time: string
  host: string
  event: string
  severity: 'info' | 'warning' | 'critical'
  message: string
  model?: string
  sent: boolean
  error?: string
  channels?: string[]
}

export interface GPUPower {
  index: number
  limit_w: number
  default_w: number
  min_w: number
  max_w: number
  persisted: boolean
}

export interface RouterSettings {
  models_max: number
  sleep_idle_seconds: number
  models_autoload: boolean
  api_key: boolean
  host: string
  port: string
  exec_start: string
}

export interface ScheduleRule {
  id: string
  enabled: boolean
  time: string
  days: number[]
  tz: string
  action: 'load' | 'unload' | 'unload-all'
  model?: string
  last_run?: string
  last_result?: string
}

export interface SweepStep {
  watts: number
  gen_tps: number
  prompt_tps: number
  ttft_ms: number
  avg_power_w: number
  max_temp_c: number
  avg_clock_mhz: number
  max_fan_pct: number
  tokens_per_joule: number
  error?: string
}

export interface Sweep {
  id: number
  request: { model: string; gpus: number[]; min_w: number; max_w: number; step_w: number; prompt_tokens: number; gen_tokens: number }
  preset: string
  build: string
  gpu_names: string[]
  original: Record<string, number>
  started: string
  finished?: string
  status: 'running' | 'done' | 'cancelled' | 'failed'
  error?: string
  plan: number[]
  steps: SweepStep[]
  picks?: Record<'max' | 'balanced' | 'efficient', { watts: number; reason: string }>
}

export interface DoctorCheck {
  id: string
  title: string
  status: 'ok' | 'warn' | 'fail'
  detail?: string
  fix?: string
}

export class ApiError extends Error {
  constructor(
    message: string,
    public status: number,
  ) {
    super(message)
  }
}

async function request<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) },
  })
  const text = await res.text()
  let data: any = null
  try {
    data = text ? JSON.parse(text) : null
  } catch {
    /* not JSON */
  }
  if (!res.ok) {
    if (res.status === 401 && data?.login) loginRequired?.()
    throw new ApiError(data?.error ?? (text || res.statusText), res.status)
  }
  return data as T
}

// Called when the server wants a password login (password_hash set and no valid session).
let loginRequired: (() => void) | undefined
export function onLoginRequired(fn: () => void) {
  loginRequired = fn
}

export const login = (password: string) => request<void>('/api/login', { method: 'POST', body: JSON.stringify({ password }) })
export const logout = () => request<void>('/api/logout', { method: 'POST', body: '{}' })
// logout: logged in with the password (only the server's own listener answers this)
export const session = () => request<{ logout: boolean }>('/api/session')

export const listHosts = () => request<HostInfo[]>('/api/hosts')

export function hostApi(host: string) {
  const base = `/api/hosts/${encodeURIComponent(host)}`
  const get = <T>(p: string) => request<T>(base + p)
  const post = <T>(p: string, body?: unknown) =>
    request<T>(base + p, { method: 'POST', body: body === undefined ? undefined : JSON.stringify(body) })
  return {
    base,
    info: () => get<Info>('/info'),
    status: () => get<Status>('/status'),
    models: () => get<{ data: Model[] }>('/models').then((r) => r.data),
    load: (model: string) => post('/models/load', { model }),
    unload: (model: string) => post('/models/unload', { model }),
    args: () => get<ArgCatalog>('/args'),
    presets: () => get<PresetFile>('/presets'),
    parse: (text: string) => post<PresetDoc>('/presets/parse', { text }),
    edit: (body: { text: string; op: 'upsert' | 'delete' | 'rename'; section: string; entries?: KV[]; new_name?: string }) =>
      post<PresetDoc>('/presets/edit', body),
    preview: (text: string) => post<{ preview: Preview; ok: boolean }>('/presets/preview', { text }),
    apply: (text: string, base_hash: string, summary: string) =>
      post<ApplyResult>('/presets/apply', { text, base_hash, summary }),
    applies: () => get<ApplyRecord[]>('/applies'),
    backups: () => get<{ name: string; time: string; size: number }[]>('/presets/backups'),
    backup: (name: string) => get<{ name: string; text: string }>(`/presets/backups/${encodeURIComponent(name)}`),
    applyDetail: (id: number) => get<ApplyRecord>(`/applies/${id}`),
    logs: (n = 200) => get<{ lines: string[] }>(`/logs?n=${n}`),
    restart: () => post('/service/restart'),
    hfSearch: (q: string) => get<HFRepo[]>(`/hf/search?q=${encodeURIComponent(q)}&limit=40`),
    hfRepo: (id: string) => get<HFDetail>(`/hf/repo?id=${encodeURIComponent(id)}`),
    local: () => get<LocalResp>('/local'),
    download: (model: string, preset?: { ctx?: number; device?: string; embeddings?: boolean }) => post('/models/download', { model, preset }),
    deleteModel: (model: string) => post('/models/delete', { model }),
    recommend: (refresh = false) =>
      get<{ catalog: { version: string; source: string }; results: Recommendation[] }>(`/recommend${refresh ? '?refresh=1' : ''}`),
    llamacpp: () =>
      get<{ active_binary: string; version: string; builds: LlamaBuild[]; can_build: boolean; flavor: string; cuda: string }>('/llamacpp'),
    llamacppReleases: () => get<LlamaRelease[]>('/llamacpp/releases'),
    llamacppUpdate: () => get<UpdateInfo>('/llamacpp/update'),
    llamacppVersions: () => get<LlamaRelease[]>('/llamacpp/versions'),
    llamacppOp: (op: 'build' | 'fetch' | 'cancel-build' | 'remove-build' | 'switch', ref: string) =>
      post<{ output: string }>(`/llamacpp/${op}`, { ref }),
    llamacppLogStream: (ref: string) => new EventSource(`${base}/llamacpp/log?ref=${encodeURIComponent(ref)}&follow=1`),
    routerSettings: () => get<RouterSettings>('/router/settings'),
    setRouterSetting: (key: string, value: string) => post<{ output: string }>('/router/settings', { key, value }),
    routerKey: () => get<{ key: string }>('/router/key'),
    gpuPower: () => get<GPUPower[]>('/gpu/power'),
    setGpuPower: (index: number, watts: number) => post<{ output: string }>('/gpu/power', { index, watts }),
    schedules: () => get<ScheduleRule[]>('/schedules'),
    saveSchedules: (rs: ScheduleRule[]) => request<ScheduleRule[]>(`${base}/schedules`, { method: 'PUT', body: JSON.stringify(rs) }),
    runSchedule: (id: string) => post<{ result: string }>(`/schedules/${id}/run`),
    alerts: () => get<{ settings: AlertSettings; recent: AlertEvent[] }>('/alerts'),
    saveAlerts: (st: AlertSettings) => request<AlertSettings>(`${base}/alerts/settings`, { method: 'PUT', body: JSON.stringify(st) }),
    testAlert: () => post<AlertEvent>('/alerts/test'),
    usage: (days: number) =>
      get<{ days: number; bucket_seconds: number; models: ModelUsage[]; series: UsageBucket[] }>(`/usage?days=${days}`),
    usageCleanup: (days: number) =>
      get<{ days: number; history_since: string; models: CleanupCandidate[] }>(`/usage/cleanup?days=${days}`),
    sweep: () => get<{ current: Sweep | null; saved: Sweep[] }>('/sweep'),
    startSweep: (req: { model: string; gpus: number[]; min_w: number; max_w: number; step_w: number; prompt_tokens: number; gen_tokens: number }) =>
      post<Sweep>('/sweep', req),
    cancelSweep: () => post<{ cancelled: boolean }>('/sweep/cancel'),
    bench: (model: string, prompt_tokens: number, gen_tokens: number, restore_loaded: boolean) =>
      post<{ id: number; result: BenchResult }>('/bench', { model, prompt_tokens, gen_tokens, restore_loaded }),
    benches: () => get<{ id: number; result: BenchResult }[]>('/bench'),
    deleteBench: (id: number) => request(`${base}/bench/${id}`, { method: 'DELETE' }),
    vram: (text: string, section: string, entries?: KV[]) =>
      post<{ model: string; estimate: VramEstimate; ram?: { available: number; total: number } }>('/vram', { text, section, entries }),
    vramFit: (text: string, section: string, entries?: KV[]) => post<{ model: string; fit: VramFit }>('/vram/fit', { text, section, entries }),
    exportBundle: (secrets: boolean) => get<Record<string, any>>(`/export${secrets ? '?secrets=1' : ''}`),
    importBundle: (bundle: unknown, parts: string[]) => post<{ imported: string[]; errors: string[] }>('/import', { bundle, parts }),
    doctor: () => get<{ checks: DoctorCheck[]; status: 'ok' | 'warn' | 'fail' }>('/doctor'),
    events: () => new EventSource(base + '/events'),
    logStream: (n = 200) => new EventSource(`${base}/logs?follow=1&n=${n}`),
  }
}

export type HostApi = ReturnType<typeof hostApi>
