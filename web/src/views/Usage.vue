<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import type { CleanupCandidate, ModelUsage, UsageBucket } from '../api'
import { api, fmtBytes, refreshModels } from '../state'

const days = ref(7)
const metric = ref<'requests' | 'gen_tokens' | 'prompt_tokens'>('requests')
const models = ref<ModelUsage[]>([])
const series = ref<UsageBucket[]>([])
const bucketSec = ref(86400)
const error = ref('')
const loading = ref(true)
// colour follows the model, not its rank: fixed by name over the last 90 days
const colorOrder = ref<string[]>([])

async function load() {
  loading.value = true
  try {
    const r = await api.value!.usage(days.value)
    models.value = r.models
    series.value = r.series
    bucketSec.value = r.bucket_seconds
    error.value = ''
  } catch (e: any) {
    error.value = e.message
  } finally {
    loading.value = false
  }
}
onMounted(async () => {
  try {
    colorOrder.value = (await api.value!.usage(90)).models.map((m) => m.model).sort()
  } catch {
    /* fall back to the current range */
  }
  await load()
})
watch(days, load)

const MAX_SERIES = 7 // slot 8 is "Other"
const keys = computed(() => {
  const ranked = [...models.value].sort((a, b) => b.requests - a.requests).map((m) => m.model)
  const shown = ranked.slice(0, ranked.length > MAX_SERIES + 1 ? MAX_SERIES : MAX_SERIES + 1)
  return { shown, other: ranked.length > shown.length }
})
function colorOf(model: string) {
  if (model === 'Other') return 'var(--series-8)'
  const order = colorOrder.value.length ? colorOrder.value : [...keys.value.shown].sort()
  const i = order.indexOf(model)
  return `var(--series-${(i < 0 ? 0 : i % 7) + 1})`
}
const short = (id: string) => id.replace(/^[^/]+\//, '')

// buckets → [{ start, stacks: [{model, value}] }]
const bars = computed(() => {
  const step = bucketSec.value * 1000
  const now = Date.now()
  const first = Math.floor((now - days.value * 864e5) / step) * step
  const map = new Map<number, Record<string, number>>()
  for (let t = first; t <= now; t += step) map.set(t, {})
  for (const b of series.value) {
    const t = new Date(b.start).getTime()
    const k = [...map.keys()].reduce((best, x) => (Math.abs(x - t) < Math.abs(best - t) ? x : best), first)
    const name = keys.value.shown.includes(b.model) ? b.model : 'Other'
    const rec = map.get(k)!
    rec[name] = (rec[name] ?? 0) + b[metric.value]
  }
  const names = [...keys.value.shown, ...(keys.value.other ? ['Other'] : [])]
  return [...map.entries()].map(([start, rec]) => ({
    start,
    stacks: names.filter((n) => rec[n]).map((n) => ({ model: n, value: rec[n] })),
    total: names.reduce((a, n) => a + (rec[n] ?? 0), 0),
  }))
})
const maxTotal = computed(() => Math.max(1, ...bars.value.map((b) => b.total)))
// round tick step (1-2-5 × 10^k) giving ~4 intervals
const tickStep = computed(() => {
  const raw = maxTotal.value / 4
  const p = 10 ** Math.floor(Math.log10(raw))
  // counts are whole numbers: never a step below 1 (it would repeat tick labels)
  return Math.max(1, [1, 2, 5, 10].map((f) => f * p).find((v) => v >= raw) ?? raw)
})
const niceMax = computed(() => Math.max(tickStep.value, Math.ceil(maxTotal.value / tickStep.value) * tickStep.value))

// geometry
const W = 900
const H = 220
const PAD = { l: 48, r: 8, t: 8, b: 24 }
const plotW = W - PAD.l - PAD.r
const plotH = H - PAD.t - PAD.b
const barW = computed(() => Math.max(2, Math.min(28, plotW / bars.value.length - 4)))
const x = (i: number) => PAD.l + (i + 0.5) * (plotW / bars.value.length)
const y = (v: number) => PAD.t + plotH - (v / niceMax.value) * plotH
const ticks = computed(() => Array.from({ length: Math.round(niceMax.value / tickStep.value) + 1 }, (_, i) => i * tickStep.value))

// segment path: rounded top (4px) only on the topmost segment; 2px surface gap between segments
function segments(b: (typeof bars.value)[number]) {
  let acc = 0
  return b.stacks.map((s, i) => {
    const y0 = y(acc)
    acc += s.value
    const y1 = y(acc)
    const top = i === b.stacks.length - 1
    const h = Math.max(0, y0 - y1 - (i > 0 ? 2 : 0))
    return { ...s, y: y1, h, top }
  })
}
function path(cx: number, w: number, yTop: number, h: number, round: boolean) {
  const x0 = cx - w / 2
  const r = round ? Math.min(4, w / 2, h) : 0
  return `M${x0},${yTop + h} V${yTop + r} Q${x0},${yTop} ${x0 + r},${yTop} H${x0 + w - r} Q${x0 + w},${yTop} ${x0 + w},${yTop + r} V${yTop + h} Z`
}

const hover = ref<number | null>(null)
const fmtN = (n: number) => (n >= 1e6 ? `${(n / 1e6).toFixed(1)}M` : n >= 1e4 ? `${(n / 1e3).toFixed(0)}k` : n >= 1e3 ? `${(n / 1e3).toFixed(1)}k` : String(Math.round(n)))
const fmtBucket = (t: number) =>
  bucketSec.value < 86400
    ? new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
    : new Date(t).toLocaleDateString([], { month: 'short', day: 'numeric' })
const labelEvery = computed(() => Math.ceil(bars.value.length / 10))

const totals = computed(() => ({
  requests: models.value.reduce((a, m) => a + m.requests, 0),
  gen: models.value.reduce((a, m) => a + m.gen_tokens, 0),
  prompt: models.value.reduce((a, m) => a + m.prompt_tokens, 0),
}))
const ago = (s: string) => {
  const m = (Date.now() - new Date(s).getTime()) / 6e4
  return m < 60 ? `${Math.max(1, Math.round(m))} min ago` : m < 2880 ? `${Math.round(m / 60)} h ago` : `${Math.round(m / 1440)} d ago`
}
// ---- CSV export ----
function csvCell(v: string | number) {
  const t = String(v)
  return /[",\n]/.test(t) ? `"${t.replace(/"/g, '""')}"` : t
}
function download(name: string, rows: (string | number)[][]) {
  const a = document.createElement('a')
  a.href = URL.createObjectURL(new Blob([rows.map((r) => r.map(csvCell).join(',')).join('\n') + '\n'], { type: 'text/csv' }))
  a.download = name
  a.click()
  URL.revokeObjectURL(a.href)
}
const stamp = () => new Date().toISOString().slice(0, 10)
function exportTotals() {
  download(`usage-models-${days.value}d-${stamp()}.csv`, [
    ['model', 'requests', 'prompt_tokens', 'gen_tokens', 'prompt_tps', 'gen_tps', 'last_used'],
    ...models.value.map((m) => [m.model, m.requests, m.prompt_tokens, m.gen_tokens, m.prompt_tps.toFixed(1), m.gen_tps.toFixed(2), m.last_used]),
  ])
}
function exportSeries() {
  download(`usage-${bucketSec.value < 86400 ? 'hourly' : 'daily'}-${days.value}d-${stamp()}.csv`, [
    ['start', 'model', 'requests', 'prompt_tokens', 'gen_tokens'],
    ...series.value.map((b) => [b.start, b.model, b.requests, b.prompt_tokens, b.gen_tokens]),
  ])
}

// ---- cleanup: downloaded models nobody used for a while ----
const idleDays = ref(30)
const cleanup = ref<CleanupCandidate[]>([])
const historySince = ref('')
const cleanupErr = ref('')
const deleting = ref('')
async function loadCleanup() {
  try {
    const r = await api.value!.usageCleanup(idleDays.value)
    cleanup.value = r.models
    historySince.value = r.history_since
    cleanupErr.value = ''
  } catch (e: any) {
    cleanupErr.value = e.message
  }
}
onMounted(loadCleanup)
watch(idleDays, loadCleanup)
const suggested = computed(() => cleanup.value.filter((c) => c.suggest))
const reclaim = computed(() => suggested.value.reduce((a, c) => a + c.size, 0))
const historyShort = computed(() => !historySince.value || new Date(historySince.value).getTime() > Date.now() - idleDays.value * 864e5)
async function remove(c: CleanupCandidate) {
  if (!confirm(`Delete ${c.id} from disk (${fmtBytes(c.size)})? This cannot be undone.`)) return
  deleting.value = c.id
  cleanupErr.value = ''
  try {
    await api.value!.deleteModel(c.id)
    await loadCleanup()
    refreshModels()
  } catch (e: any) {
    cleanupErr.value = `${c.id}: ${e.message}`
  } finally {
    deleting.value = ''
  }
}
const lastUse = (c: CleanupCandidate) => (c.last_used ? ago(c.last_used) : historySince.value ? `not since ${new Date(historySince.value).toLocaleDateString()}` : 'never')

const metricLabel = { requests: 'Requests', gen_tokens: 'Generated tokens', prompt_tokens: 'Prompt tokens' }
</script>

<template>
  <div class="space-y-4">
    <!-- filters: one row above the charts -->
    <div class="flex flex-wrap items-center gap-2">
      <div class="flex overflow-hidden rounded-lg border border-zinc-300 text-sm dark:border-zinc-700">
        <button
          v-for="d in [1, 7, 30, 90]"
          :key="d"
          class="px-3 py-1.5"
          :class="days === d ? 'bg-zinc-900 text-white dark:bg-zinc-100 dark:text-zinc-900' : 'hover:bg-zinc-100 dark:hover:bg-zinc-800'"
          @click="days = d"
        >
          {{ d === 1 ? '24 h' : `${d} d` }}
        </button>
      </div>
      <select v-model="metric" class="input !w-auto !py-1.5">
        <option value="requests">Requests</option>
        <option value="gen_tokens">Generated tokens</option>
        <option value="prompt_tokens">Prompt tokens</option>
      </select>
      <span v-if="loading" class="muted text-xs">loading…</span>
      <div class="ml-auto flex gap-2">
        <button class="btn btn-sm" :disabled="!models.length" title="Per-model totals for this period" @click="exportTotals">Export totals CSV</button>
        <button class="btn btn-sm" :disabled="!series.length" title="Requests and tokens per model and hour/day" @click="exportSeries">Export series CSV</button>
      </div>
    </div>
    <div v-if="error" class="rounded-lg bg-red-50 px-4 py-2 text-sm text-red-700 dark:bg-red-950/50 dark:text-red-300">{{ error }}</div>

    <!-- headline numbers -->
    <div class="grid gap-4 sm:grid-cols-3">
      <div class="card p-4">
        <div class="text-xs font-medium uppercase tracking-wide text-zinc-500">Requests</div>
        <div class="mt-1 text-2xl font-semibold tabular-nums">{{ totals.requests.toLocaleString() }}</div>
      </div>
      <div class="card p-4">
        <div class="text-xs font-medium uppercase tracking-wide text-zinc-500">Generated tokens</div>
        <div class="mt-1 text-2xl font-semibold tabular-nums">{{ fmtN(totals.gen) }}</div>
      </div>
      <div class="card p-4">
        <div class="text-xs font-medium uppercase tracking-wide text-zinc-500">Prompt tokens</div>
        <div class="mt-1 text-2xl font-semibold tabular-nums">{{ fmtN(totals.prompt) }}</div>
      </div>
    </div>

    <!-- stacked bars -->
    <div class="card p-4">
      <div class="mb-2 flex flex-wrap items-baseline justify-between gap-2">
        <div class="text-sm font-semibold">{{ metricLabel[metric] }} per {{ bucketSec < 86400 ? 'hour' : 'day' }}</div>
        <!-- legend (always present for ≥ 2 series) -->
        <div class="flex flex-wrap gap-x-3 gap-y-1 text-xs text-zinc-600 dark:text-zinc-300">
          <span v-for="m in [...keys.shown, ...(keys.other ? ['Other'] : [])]" :key="m" class="flex items-center gap-1.5">
            <span class="h-2.5 w-2.5 rounded-sm" :style="{ background: colorOf(m) }" />{{ short(m) }}
          </span>
        </div>
      </div>
      <div class="relative">
        <svg :viewBox="`0 0 ${W} ${H}`" class="h-auto w-full" @mouseleave="hover = null">
          <g class="text-zinc-400 dark:text-zinc-500">
            <template v-for="t in ticks" :key="t">
              <line :x1="PAD.l" :x2="W - PAD.r" :y1="y(t)" :y2="y(t)" stroke="currentColor" stroke-opacity="0.25" stroke-width="1" />
              <text :x="PAD.l - 6" :y="y(t) + 4" text-anchor="end" font-size="11" fill="currentColor">{{ fmtN(t) }}</text>
            </template>
          </g>
          <g v-for="(b, i) in bars" :key="b.start">
            <path
              v-for="s in segments(b)"
              :key="s.model"
              :d="path(x(i), barW, s.y, s.h, s.top)"
              :fill="colorOf(s.model)"
              :opacity="hover === null || hover === i ? 1 : 0.45"
            />
            <!-- hit target wider than the mark -->
            <rect :x="x(i) - plotW / bars.length / 2" :y="PAD.t" :width="plotW / bars.length" :height="plotH" fill="transparent" @mouseenter="hover = i" />
            <text v-if="i % labelEvery === 0" :x="x(i)" :y="H - 6" text-anchor="middle" font-size="11" class="fill-zinc-500 dark:fill-zinc-400">
              {{ fmtBucket(b.start) }}
            </text>
          </g>
        </svg>
        <div
          v-if="hover !== null && bars[hover]"
          class="pointer-events-none absolute top-2 z-10 min-w-48 rounded-lg border border-zinc-200 bg-white px-3 py-2 text-xs shadow-lg dark:border-zinc-700 dark:bg-zinc-900"
          :style="{ left: `min(calc(${(x(hover) / W) * 100}% + 12px), calc(100% - 13rem))` }"
        >
          <div class="mb-1 font-semibold">{{ bucketSec < 86400 ? new Date(bars[hover].start).toLocaleString([], { weekday: 'short', hour: '2-digit', minute: '2-digit' }) : new Date(bars[hover].start).toLocaleDateString([], { weekday: 'short', month: 'short', day: 'numeric' }) }}</div>
          <div v-for="s in [...bars[hover].stacks].reverse()" :key="s.model" class="flex items-center gap-2">
            <span class="h-2 w-2 rounded-sm" :style="{ background: colorOf(s.model) }" />
            <span class="min-w-0 flex-1 truncate text-zinc-600 dark:text-zinc-300">{{ short(s.model) }}</span>
            <span class="tabular-nums">{{ s.value.toLocaleString() }}</span>
          </div>
          <div v-if="!bars[hover].stacks.length" class="muted">no requests</div>
        </div>
      </div>
    </div>

    <!-- table view (also the accessible alternative to the chart) -->
    <div class="card">
      <div class="card-h">Per model</div>
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="text-left text-xs text-zinc-500">
            <tr class="border-b border-zinc-200 dark:border-zinc-800">
              <th class="px-4 py-2 font-medium">Model</th>
              <th class="px-3 py-2 text-right font-medium">Requests</th>
              <th class="px-3 py-2 text-right font-medium">Prompt tokens</th>
              <th class="px-3 py-2 text-right font-medium">Generated</th>
              <th class="hidden px-3 py-2 text-right font-medium sm:table-cell">Prompt t/s</th>
              <th class="hidden px-3 py-2 text-right font-medium sm:table-cell">Gen t/s</th>
              <th class="px-4 py-2 text-right font-medium">Last used</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="m in models" :key="m.model" class="border-b border-zinc-100 last:border-0 dark:border-zinc-800/60">
              <td class="px-4 py-2">
                <span class="mr-2 inline-block h-2.5 w-2.5 rounded-sm align-middle" :style="{ background: colorOf(keys.shown.includes(m.model) ? m.model : 'Other') }" />
                <span class="font-mono text-xs break-all">{{ m.model }}</span>
              </td>
              <td class="px-3 py-2 text-right tabular-nums">{{ m.requests.toLocaleString() }}</td>
              <td class="px-3 py-2 text-right tabular-nums">{{ fmtN(m.prompt_tokens) }}</td>
              <td class="px-3 py-2 text-right tabular-nums">{{ fmtN(m.gen_tokens) }}</td>
              <td class="hidden px-3 py-2 text-right tabular-nums sm:table-cell">{{ Math.round(m.prompt_tps) }}</td>
              <td class="hidden px-3 py-2 text-right tabular-nums sm:table-cell">{{ m.gen_tps.toFixed(1) }}</td>
              <td class="muted px-4 py-2 text-right text-xs whitespace-nowrap">{{ ago(m.last_used) }}</td>
            </tr>
            <tr v-if="!models.length && !loading">
              <td colspan="7" class="muted px-4 py-8 text-center">No requests in this period.</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="muted border-t border-zinc-200 px-4 py-2 text-xs dark:border-zinc-800">
        Parsed from the llama-server journal (request timings and the router's routing lines); kept for 90 days.
      </p>
    </div>

    <!-- cleanup -->
    <div class="card">
      <div class="card-h">
        <span>Cleanup</span>
        <label class="flex items-center gap-1.5 text-xs font-normal">
          unused for
          <select v-model.number="idleDays" class="input !w-auto !py-0.5 text-xs">
            <option v-for="d in [7, 14, 30, 60, 90]" :key="d" :value="d">{{ d }} days</option>
          </select>
        </label>
      </div>
      <div v-if="cleanupErr" class="px-4 py-2 text-sm text-red-600">{{ cleanupErr }}</div>
      <p v-if="historyShort" class="muted px-4 pt-3 text-xs">
        Usage history only goes back to {{ historySince ? new Date(historySince).toLocaleDateString() : 'today' }}, so models without any request are not suggested yet.
      </p>
      <p v-if="suggested.length" class="px-4 pt-3 text-sm">
        {{ suggested.length }} downloaded model{{ suggested.length === 1 ? '' : 's' }} not used for {{ idleDays }} days · {{ fmtBytes(reclaim) }} on disk.
      </p>
      <p v-else-if="cleanup.length" class="muted px-4 pt-3 text-sm">Every downloaded model was used in the last {{ idleDays }} days.</p>
      <ul class="mt-2">
        <li
          v-for="c in cleanup"
          :key="c.id"
          class="flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-zinc-100 px-4 py-2 text-sm dark:border-zinc-800/60"
          :class="c.suggest ? '' : 'opacity-60'"
        >
          <span class="w-full min-w-0 font-mono text-xs break-all sm:w-auto sm:flex-1">{{ c.id }}</span>
          <span class="muted text-xs tabular-nums">{{ fmtBytes(c.size) }}</span>
          <span class="muted text-xs sm:w-36 sm:text-right">{{ lastUse(c) }}</span>
          <span v-if="c.status && c.status !== 'unloaded'" class="badge bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300">{{ c.status }}</span>
          <span v-if="c.presets.length" class="muted text-xs" :title="`Used by preset(s): ${c.presets.join(', ')}. Remove them before deleting.`">
            {{ c.presets.length }} preset{{ c.presets.length === 1 ? '' : 's' }}
          </span>
          <button
            v-if="c.suggest"
            class="btn btn-sm ml-auto text-red-600 sm:ml-0"
            :disabled="!!deleting || c.presets.length > 0"
            :title="c.presets.length ? `Used by ${c.presets.join(', ')}: remove those presets first` : 'Delete from disk'"
            @click="remove(c)"
          >
            {{ deleting === c.id ? 'Deleting…' : 'Delete' }}
          </button>
        </li>
      </ul>
    </div>
  </div>
</template>
