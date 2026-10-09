<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { BenchResult, LlamaBuild, LlamaRelease } from '../api'
import RouterSettings from '../components/RouterSettings.vue'
import { api, isRunning, refreshModels, refreshStatus, refreshUpdate, state } from '../state'

const builds = ref<LlamaBuild[]>([])
const activeBinary = ref('')
const version = ref('')
const canBuild = ref(false)
const flavor = ref('')
const releases = ref<LlamaRelease[] | null>(null) // nightly builds (bNNNNN)
const versions = ref<LlamaRelease[]>([]) // version releases (v0.6.0, …), each with its build tag
// nightly builds are hidden unless asked for (remembered in this browser)
const SHOW_BUILDS_KEY = 'llamira.showBuilds'
const showBuilds = ref(false)
try {
  showBuilds.value = localStorage.getItem(SHOW_BUILDS_KEY) === '1'
} catch {
  /* storage unavailable */
}
watch(showBuilds, (v) => {
  try {
    localStorage.setItem(SHOW_BUILDS_KEY, v ? '1' : '0')
  } catch {
    /* ignore */
  }
})
const buildNum = (tag: string) => Number(tag.match(/^b(\d+)/)?.[1] ?? 0)
const versionByTag = computed(() => Object.fromEntries(versions.value.map((v) => [v.tag, v.version!])))
// versions first-class; with "show builds", the nightlies merged in by build number (a nightly
// that is a version's build shows as that version)
const shown = computed(() => {
  const rows = [...versions.value]
  if (showBuilds.value) for (const r of releases.value ?? []) if (!versionByTag.value[r.tag]) rows.push(r)
  return rows.sort((a, b) => buildNum(b.tag) - buildNum(a.tag))
})
const relError = ref('')
const error = ref('')
const notice = ref('')
const busy = ref('')
const logRef = ref('')
const logLines = ref<string[]>([])
const logBox = ref<HTMLElement | null>(null)
let es: EventSource | null = null
let timer: number | undefined

async function load() {
  try {
    const r = await api.value!.llamacpp()
    builds.value = r.builds ?? []
    activeBinary.value = r.active_binary
    version.value = r.version
    canBuild.value = r.can_build
    flavor.value = r.flavor
    const running = r.builds.find((b) => b.running)
    if (running && logRef.value !== running.ref) openLog(running.ref)
  } catch (e: any) {
    error.value = e.message
  }
}

onMounted(async () => {
  await load()
  timer = window.setInterval(load, 5000)
  const [v, r] = await Promise.allSettled([api.value!.llamacppVersions(), api.value!.llamacppReleases()])
  if (v.status === 'fulfilled') versions.value = v.value
  if (r.status === 'fulfilled') releases.value = r.value
  else if (v.status === 'rejected') relError.value = v.reason?.message ?? String(v.reason)
  if (v.status === 'rejected' && r.status === 'fulfilled') showBuilds.value = true // no versions: fall back to builds
  if (!releases.value) releases.value = []
})
onBeforeUnmount(() => {
  window.clearInterval(timer)
  es?.close()
})

const active = computed(() => builds.value.find((b) => b.active))
const byRef = computed(() => Object.fromEntries(builds.value.map((b) => [b.ref, b])))
const anyRunning = computed(() => builds.value.some((b) => b.running))

function openLog(ref: string) {
  es?.close()
  logRef.value = ref
  logLines.value = []
  es = api.value!.llamacppLogStream(ref)
  es.addEventListener('log', (e) => {
    logLines.value.push(JSON.parse((e as MessageEvent).data))
    if (logLines.value.length > 3000) logLines.value.splice(0, 500)
  })
}

watch(
  () => logLines.value.length,
  async () => {
    const el = logBox.value
    if (!el) return
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 60
    await nextTick()
    if (atBottom) el.scrollTop = el.scrollHeight
  },
)

async function op(kind: 'build' | 'fetch' | 'cancel-build' | 'remove-build' | 'switch', ref: string) {
  const msgs: Record<string, string> = {
    fetch: `Download the prebuilt llama.cpp ${ref} (${flavor.value}) from GitHub? Takes a few minutes; the running router is not touched.`,
    build: `Build llama.cpp ${ref}? This compiles llama-server with your current CUDA flags in the background (low priority; typically 15–40 min). The running router is not touched.`,
    'cancel-build': `Cancel the build of ${ref}?`,
    'remove-build': `Delete build ${ref} from disk?`,
    switch: `Switch the router to ${ref}? llama-server restarts (loaded models are unloaded, in-flight requests fail). If it is not healthy afterwards, the previous version is restored automatically.`,
  }
  if (!confirm(msgs[kind])) return
  busy.value = kind + ref
  error.value = ''
  notice.value = ''
  try {
    await api.value!.llamacppOp(kind, ref)
    if (kind === 'build' || kind === 'fetch') openLog(ref)
    if (kind === 'switch') {
      notice.value = `Router now runs ${ref}.`
      refreshStatus()
      await refreshModels()
      refreshUpdate()
      offerRecheck(ref)
    }
    await load()
  } catch (e: any) {
    error.value = e.message
  } finally {
    busy.value = ''
  }
}

// After a switch: re-run the latest benchmark of the loaded model (or the latest one at all) on
// the new build and show old vs new, to catch speed regressions.
const recheck = ref<{ ref: string; before: BenchResult; after?: BenchResult; running?: boolean; error?: string } | null>(null)
async function offerRecheck(ref: string) {
  recheck.value = null
  try {
    const list = (await api.value!.benches()).map((b) => b.result).filter((r) => !r.error)
    const loaded = new Set(state.models.filter(isRunning).map((m) => m.id))
    const before = list.find((r) => loaded.has(r.model)) ?? list[0]
    if (before) recheck.value = { ref, before }
  } catch {
    /* no benchmarks: nothing to offer */
  }
}
async function runRecheck() {
  const rc = recheck.value
  if (!rc) return
  rc.running = true
  rc.error = undefined
  try {
    const r = await api.value!.bench(rc.before.model, rc.before.prompt_tokens || rc.before.prompt_n, rc.before.gen_tokens || rc.before.gen_n, true)
    if (r.result.error) rc.error = r.result.error
    else rc.after = r.result
  } catch (e: any) {
    rc.error = e.message
  } finally {
    rc.running = false
    refreshModels()
  }
}
const pct = (a: number, b: number) => {
  const d = ((b - a) / a) * 100
  return `${d >= 0 ? '+' : ''}${d.toFixed(1)}%`
}
const tone = (a: number, b: number) =>
  b < a * 0.97 ? 'text-red-600 dark:text-red-400' : b > a * 1.03 ? 'text-emerald-600 dark:text-emerald-400' : 'muted'

const fmt = (s?: string) => (s && !s.startsWith('0001') ? new Date(s).toLocaleString() : '')
const ago = (s: string) => {
  const h = (Date.now() - new Date(s).getTime()) / 3.6e6
  return h < 48 ? `${Math.max(1, Math.round(h))} h ago` : `${Math.round(h / 24)} d ago`
}
</script>

<template>
  <div class="space-y-4">
    <div class="card p-4">
      <div class="text-xs font-medium uppercase tracking-wide text-zinc-500">Running version</div>
      <div class="mt-1 text-lg font-semibold">
        <template v-if="state.update?.current_version">
          {{ state.update.current_version }}<span v-if="state.update.builds_ahead" class="muted text-sm font-normal"> + {{ state.update.builds_ahead }} builds</span>
          <span class="muted ml-1 text-sm font-normal">({{ version }})</span>
        </template>
        <template v-else>{{ version || '…' }}</template>
      </div>
      <div class="muted mt-0.5 font-mono text-xs break-all">{{ activeBinary }}</div>
      <div v-if="active" class="muted mt-0.5 text-xs">build: {{ active.original ? 'original (/opt/llama.cpp/build)' : active.ref }}</div>
    </div>
    <div
      v-if="state.update?.newer"
      class="flex flex-wrap items-center gap-2 rounded-lg bg-sky-50 px-4 py-2 text-sm text-sky-900 dark:bg-sky-950/50 dark:text-sky-200"
    >
      <span>
        <b>{{ state.update.latest_version }}</b> ({{ state.update.latest }}) is available (running
        {{ state.update.current_version ? `${state.update.current_version}${state.update.builds_ahead ? ` + ${state.update.builds_ahead} builds` : ''}` : state.update.current }}).
      </span>
      <span v-if="state.update.notes" class="muted min-w-0 truncate text-xs">{{ state.update.notes }}</span>
      <a v-if="state.update.url" :href="state.update.url" target="_blank" rel="noopener" class="ml-auto text-xs underline">release notes ↗</a>
    </div>
    <div v-if="recheck" class="card p-4 text-sm">
      <div class="mb-2 flex flex-wrap items-center gap-2">
        <span class="font-semibold">Speed check on {{ recheck.ref }}</span>
        <span class="muted text-xs">
          {{ recheck.before.model }} · {{ recheck.before.prompt_tokens || recheck.before.prompt_n }} prompt / {{ recheck.before.gen_tokens || recheck.before.gen_n }} gen tokens
        </span>
        <div class="ml-auto flex gap-2">
          <button v-if="!recheck.after" class="btn btn-sm btn-primary" :disabled="recheck.running" @click="runRecheck">
            {{ recheck.running ? 'Running…' : 'Run the same benchmark' }}
          </button>
          <button class="btn btn-sm" :disabled="recheck.running" @click="recheck = null">Dismiss</button>
        </div>
      </div>
      <p v-if="!recheck.after && !recheck.running" class="muted text-xs">
        Compares with the result from {{ new Date(recheck.before.time).toLocaleString() }}. Loading the model may unload another one; it is reloaded afterwards.
      </p>
      <p v-if="recheck.error" class="text-xs text-red-600">{{ recheck.error }}</p>
      <table v-if="recheck.after" class="text-xs tabular-nums">
        <thead>
          <tr class="muted"><th class="pr-4 text-left font-medium" /><th class="pr-4 text-right font-medium">before</th><th class="pr-4 text-right font-medium">{{ recheck.ref }}</th><th class="text-right font-medium">change</th></tr>
        </thead>
        <tbody>
          <tr>
            <td class="pr-4">prompt t/s</td><td class="pr-4 text-right">{{ recheck.before.prompt_tps.toFixed(0) }}</td><td class="pr-4 text-right">{{ recheck.after.prompt_tps.toFixed(0) }}</td>
            <td class="text-right" :class="tone(recheck.before.prompt_tps, recheck.after.prompt_tps)">{{ pct(recheck.before.prompt_tps, recheck.after.prompt_tps) }}</td>
          </tr>
          <tr>
            <td class="pr-4">generation t/s</td><td class="pr-4 text-right">{{ recheck.before.gen_tps.toFixed(1) }}</td><td class="pr-4 text-right">{{ recheck.after.gen_tps.toFixed(1) }}</td>
            <td class="text-right" :class="tone(recheck.before.gen_tps, recheck.after.gen_tps)">{{ pct(recheck.before.gen_tps, recheck.after.gen_tps) }}</td>
          </tr>
        </tbody>
      </table>
    </div>
    <RouterSettings />
    <div v-if="error" class="rounded-lg bg-red-50 px-4 py-2 text-sm whitespace-pre-wrap text-red-700 dark:bg-red-950/50 dark:text-red-300">{{ error }}</div>
    <div v-if="notice" class="rounded-lg bg-emerald-50 px-4 py-2 text-sm text-emerald-800 dark:bg-emerald-950/50 dark:text-emerald-300">{{ notice }}</div>

    <div class="grid gap-4 lg:grid-cols-2">
      <!-- releases -->
      <div class="card">
        <div class="card-h">
          Upstream releases
          <div class="flex items-center gap-3 text-xs font-normal">
            <label class="flex items-center gap-1.5" title="Also list the nightly builds (bNNNNN) between versions">
              <input v-model="showBuilds" type="checkbox" /> Show builds
            </label>
            <a href="https://github.com/ggml-org/llama.cpp/releases" target="_blank" rel="noopener" class="muted hover:underline">GitHub ↗</a>
          </div>
        </div>
        <div v-if="relError" class="px-4 py-3 text-sm text-red-600">{{ relError }}</div>
        <div v-else-if="!releases" class="muted px-4 py-6 text-center text-sm">Loading…</div>
        <ul v-else class="max-h-[60vh] overflow-auto">
          <li v-if="!shown.length" class="muted px-4 py-6 text-center text-sm">No version releases found. Tick “Show builds” for the nightly builds.</li>
          <li v-for="r in shown" :key="r.tag" class="flex items-start gap-3 border-b border-zinc-100 px-4 py-2 last:border-0 dark:border-zinc-800/60">
            <div class="min-w-0 flex-1">
              <a :href="r.url" target="_blank" rel="noopener" class="font-mono text-sm font-medium hover:underline" :class="r.version ? '' : 'text-zinc-600 dark:text-zinc-400'">{{
                r.version ?? r.tag
              }}</a>
              <span v-if="r.version" class="muted ml-1.5 font-mono text-xs">{{ r.tag }}</span>
              <span class="muted ml-2 text-xs">{{ ago(r.published) }}</span>
              <div v-if="r.notes" class="muted line-clamp-2 text-xs">{{ r.notes }}</div>
            </div>
            <span v-if="byRef[r.tag]?.active" class="badge bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300">running</span>
            <span v-else-if="byRef[r.tag]?.running" class="badge bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300">building</span>
            <span v-else-if="byRef[r.tag]?.ok" class="badge bg-zinc-100 text-zinc-700 dark:bg-zinc-800 dark:text-zinc-300">built</span>
            <template v-else>
              <button class="btn btn-sm" :disabled="anyRunning || !!busy" :title="`Prebuilt ${flavor} build from GitHub (minutes)`" @click="op('fetch', r.tag)">Download</button>
              <button v-if="canBuild" class="btn btn-sm" :disabled="anyRunning || !!busy" title="Compile from source with this host's flags (~20 min)" @click="op('build', r.tag)">Build</button>
            </template>
          </li>
        </ul>
      </div>

      <!-- builds -->
      <div class="card">
        <div class="card-h">Builds on this host</div>
        <ul>
          <li v-for="b in builds" :key="b.ref" class="border-b border-zinc-100 px-4 py-2.5 last:border-0 dark:border-zinc-800/60">
            <div class="flex flex-wrap items-center gap-2">
              <span class="font-mono text-sm font-medium">{{ b.original ? 'original' : b.ref }}</span>
              <span v-if="!b.original && versionByTag[b.ref]" class="badge bg-sky-100 text-sky-800 dark:bg-sky-950 dark:text-sky-300">{{ versionByTag[b.ref] }}</span>
              <span v-if="b.active" class="badge bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300">running</span>
              <span v-if="b.running" class="badge bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300">building…</span>
              <span v-else-if="!b.ok" class="badge bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300">{{ b.error === 'incomplete' ? 'incomplete' : 'failed' }}</span>
              <div class="ml-auto flex gap-1.5">
                <button v-if="!b.original" class="btn btn-sm" @click="openLog(b.ref)">Log</button>
                <button v-if="b.running" class="btn btn-sm" :disabled="!!busy" @click="op('cancel-build', b.ref)">Cancel</button>
                <button v-if="b.ok && !b.active && !b.running" class="btn btn-sm btn-primary" :disabled="!!busy" @click="op('switch', b.ref)">
                  {{ busy === 'switch' + b.ref ? 'Switching…' : 'Switch to' }}
                </button>
                <button v-if="!b.original && !b.active && !b.running" class="btn btn-sm text-red-600" :disabled="!!busy" @click="op('remove-build', b.ref)">Delete</button>
              </div>
            </div>
            <div class="muted mt-0.5 text-xs">
              <template v-if="b.version">{{ b.version }} · </template>{{ fmt(b.built_at) }}<template v-if="b.source"> · {{ b.source }}</template>
            </div>
            <div v-if="b.dry_run" class="text-xs text-emerald-700 dark:text-emerald-400">✓ {{ b.dry_run }}</div>
            <div v-if="b.error && b.error !== 'incomplete'" class="text-xs break-words text-red-600">{{ b.error }}</div>
          </li>
        </ul>
        <p class="muted border-t border-zinc-200 px-4 py-2 text-xs dark:border-zinc-800">
          <b>Download</b> fetches the upstream prebuilt release for this host ({{ flavor }}); <b>Build</b> compiles from source with the flags of your current build.
          Both go to /opt/llama.cpp-builds/&lt;tag&gt; and are validated against your preset file before you can switch.
        </p>
      </div>
    </div>

    <div v-if="logRef" class="card">
      <div class="card-h">
        Build log: {{ logRef }}
        <button class="btn btn-sm" @click="(logRef = ''), es?.close()">Close</button>
      </div>
      <div ref="logBox" class="h-80 overflow-auto rounded-b-xl bg-zinc-950 px-3 py-2 font-mono text-[11px] leading-[1.5] whitespace-pre-wrap break-all text-zinc-300">
        <div v-for="(l, i) in logLines" :key="i" :class="{ 'text-emerald-400': l.includes('==>'), 'text-red-400': /error|FAILED/i.test(l) }">{{ l }}</div>
        <div v-if="!logLines.length" class="text-zinc-500">No output yet.</div>
      </div>
    </div>
  </div>
</template>
