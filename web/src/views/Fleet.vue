<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { hostApi, type Model, type Section, type Status } from '../api'
import { selectHost, state } from '../state'
import { useRouter } from 'vue-router'

interface Row {
  name: string
  label: string
  connected: boolean
  error?: string
  status?: Status
  models?: Model[]
}

const router = useRouter()
const rows = ref<Row[]>([])
let timer: number | undefined

async function load() {
  rows.value = await Promise.all(
    state.hosts.map(async (h) => {
      const row: Row = { name: h.name, label: h.label, connected: h.connected, error: h.error }
      if (!h.connected) return row
      try {
        const a = hostApi(h.name)
        const [status, models] = await Promise.all([a.status(), a.models()])
        row.status = status
        row.models = models
      } catch (e: any) {
        row.error = e.message
      }
      return row
    }),
  )
}
onMounted(() => {
  load()
  timer = window.setInterval(load, 10000)
})
onBeforeUnmount(() => window.clearInterval(timer))

async function open(name: string) {
  await selectHost(name)
  router.push('/')
}
// ---- copy a preset section from one host into another host's working copy ----
const online = computed(() => state.hosts.filter((h) => h.connected).map((h) => h.name))
const from = ref('')
const to = ref('')
const sections = ref<Section[]>([])
const section = ref('')
const copyErr = ref('')
const copying = ref(false)
watch(
  online,
  (o) => {
    if (!from.value) from.value = o[0] ?? ''
    if (!to.value) to.value = o.find((h) => h !== from.value) ?? ''
  },
  { immediate: true },
)
watch(
  from,
  async (h) => {
    sections.value = []
    section.value = ''
    if (!h) return
    try {
      sections.value = (await hostApi(h).presets()).sections.filter((s) => s.name !== '*')
      section.value = sections.value[0]?.name ?? ''
    } catch (e: any) {
      copyErr.value = e.message
    }
  },
  { immediate: true },
)
async function copyPreset() {
  const sec = sections.value.find((s) => s.name === section.value)
  if (!sec || !to.value || to.value === from.value) return
  copying.value = true
  copyErr.value = ''
  try {
    const dst = hostApi(to.value)
    const [file, local] = await Promise.all([dst.presets(), dst.local()])
    if (file.sections.some((s) => s.name === sec.name) && !confirm(`[${sec.name}] already exists on ${to.value}. Replace it in the working copy?`)) return
    const model = sec.entries.find((e) => e.key === 'hf-repo')?.value ?? (sec.entries.some((e) => e.key === 'model') ? '' : sec.name)
    if (model && !local.models.some((m) => m.id.toLowerCase() === model.toLowerCase())) {
      if (!confirm(`${model} is not downloaded on ${to.value}. The preset will not validate until it is (download it on the Models page). Copy anyway?`)) return
    }
    const edited = await dst.edit({ text: file.text, op: 'upsert', section: sec.name, entries: sec.entries })
    await selectHost(to.value)
    state.pending = { text: edited.text, label: `[${sec.name}] copied from ${from.value}` }
    router.push({ path: '/presets', query: { section: sec.name } })
  } catch (e: any) {
    copyErr.value = e.message
  } finally {
    copying.value = false
  }
}

const loaded = (r: Row) => (r.models ?? []).filter((m) => ['loaded', 'loading', 'sleeping'].includes(m.status.value))
</script>

<template>
  <div v-if="online.length > 1" class="card mb-4 p-4 text-sm">
    <div class="mb-2 font-semibold">Copy a preset to another host</div>
    <div class="flex flex-wrap items-end gap-2">
      <div>
        <label class="label">From</label>
        <select v-model="from" class="input !w-auto">
          <option v-for="h in online" :key="h" :value="h">{{ h }}</option>
        </select>
      </div>
      <div class="min-w-48 flex-1">
        <label class="label">Preset</label>
        <select v-model="section" class="input font-mono text-xs" :disabled="!sections.length">
          <option v-for="s in sections" :key="s.name" :value="s.name">{{ s.name }}</option>
        </select>
      </div>
      <div>
        <label class="label">To</label>
        <select v-model="to" class="input !w-auto">
          <option v-for="h in online.filter((x) => x !== from)" :key="h" :value="h">{{ h }}</option>
        </select>
      </div>
      <button class="btn btn-primary" :disabled="copying || !section || !to || to === from" @click="copyPreset">
        {{ copying ? 'Copying…' : 'Copy to working copy' }}
      </button>
    </div>
    <p class="muted mt-2 text-xs">Opens the target host's Presets page with the section added; nothing changes there until you review &amp; apply.</p>
    <p v-if="copyErr" class="mt-1 text-xs text-red-600">{{ copyErr }}</p>
  </div>
  <div class="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
    <div v-for="r in rows" :key="r.name" class="card p-4">
      <div class="flex items-start justify-between gap-2">
        <div>
          <div class="font-semibold">{{ r.name }}</div>
          <div class="muted text-xs">{{ r.label }}</div>
        </div>
        <span
          class="badge"
          :class="r.status?.router.healthy ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300' : 'bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300'"
        >
          {{ !r.connected ? 'unreachable' : r.status?.router.healthy ? 'healthy' : 'router down' }}
        </span>
      </div>
      <p v-if="r.error" class="mt-2 text-xs break-words text-red-600">{{ r.error }}</p>
      <template v-if="r.status">
        <div class="muted mt-2 text-xs">{{ r.status.router.props?.build_info }} · {{ (r.models ?? []).length }} models</div>
        <div class="mt-2 space-y-1.5">
          <div v-for="g in r.status.gpus ?? []" :key="g.index" class="text-xs">
            <div class="flex justify-between tabular-nums">
              <span>{{ r.status.devices?.[g.index] ?? `GPU${g.index}` }} · {{ g.temp_c }}°C · {{ Math.round(g.power_w) }} W</span>
              <span>{{ (g.mem_used_mib / 1024).toFixed(1) }}/{{ (g.mem_total_mib / 1024).toFixed(0) }} GiB</span>
            </div>
            <div class="mt-0.5 h-1.5 overflow-hidden rounded-full bg-zinc-200 dark:bg-zinc-800">
              <div class="h-full bg-emerald-500" :style="{ width: (g.mem_used_mib / g.mem_total_mib) * 100 + '%' }" />
            </div>
          </div>
        </div>
        <div v-if="r.status.host" class="mt-1.5 flex justify-between text-xs tabular-nums" :title="r.status.host.unit ? `llama.cpp: ${(r.status.host.unit.mem / 2 ** 30).toFixed(1)} GiB` : ''">
          <span>RAM {{ (r.status.host.mem_used / 2 ** 30).toFixed(1) }}/{{ (r.status.host.mem_total / 2 ** 30).toFixed(0) }} GiB<span v-if="r.status.host.swap_used > 64 * 2 ** 20" class="text-amber-600"> · swap {{ (r.status.host.swap_used / 2 ** 30).toFixed(1) }}</span></span>
          <span>CPU {{ Math.round(r.status.host.cpu_pct) }}% · load {{ r.status.host.load[0].toFixed(1) }}</span>
        </div>
        <div class="mt-3 text-xs">
          <div class="muted mb-0.5">Loaded</div>
          <div v-for="m in loaded(r)" :key="m.id" class="truncate font-mono">{{ m.id }} <span class="muted">({{ m.status.value }})</span></div>
          <div v-if="!loaded(r).length" class="muted">nothing loaded</div>
        </div>
      </template>
      <button class="btn btn-sm mt-3 w-full" :disabled="!r.connected" @click="open(r.name)">Open</button>
    </div>
  </div>
</template>
