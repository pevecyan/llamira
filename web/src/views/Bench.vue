<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import type { BenchResult, LlamaRelease } from '../api'
import GpuCard from '../components/GpuCard.vue'
import HostCard from '../components/HostCard.vue'
import PowerSweep from '../components/PowerSweep.vue'
import { api, fmtCtx, isRunning, presetValues, refreshModels, state } from '../state'

const route = useRoute()

const model = ref((route.query.model as string) ?? '')
const promptTokens = ref(2048)
const genTokens = ref(128)
const restore = ref(true)
const running = ref(false)
const runStart = ref(0)
const elapsed = ref(0)
const error = ref('')
const list = ref<{ id: number; result: BenchResult }[]>([])
const filter = ref('')

async function load() {
  try {
    list.value = await api.value!.benches()
  } catch (e: any) {
    error.value = e.message
  }
}
// version releases, to show a result's build as "v0.4.1 + 72"
const versions = ref<LlamaRelease[]>([])
onMounted(async () => {
  load()
  try {
    versions.value = await api.value!.llamacppVersions()
  } catch {
    /* the build number alone is shown */
  }
})
const buildNum = (s: string) => Number(s.match(/^b?(\d+)/)?.[1] ?? 0)
function llamaVersion(build?: string) {
  if (!build) return ''
  const n = buildNum(build)
  const tag = build.split('-')[0]
  let best: LlamaRelease | undefined
  for (const v of versions.value) {
    const vn = buildNum(v.tag)
    if (vn && vn <= n && (!best || vn > buildNum(best.tag))) best = v
  }
  if (!best) return tag
  const ahead = n - buildNum(best.tag)
  return `${best.version}${ahead ? ` + ${ahead}` : ''} · ${tag}`
}
// default to the loaded model once the model list is known (it loads asynchronously)
watch(
  () => state.models.length,
  () => {
    if (!model.value) model.value = state.models.find((m) => isRunning(m))?.id ?? state.models[0]?.id ?? ''
  },
  { immediate: true },
)

const target = computed(() => state.models.find((m) => m.id === model.value))
const willUnload = computed(() => {
  const max = state.status?.router.props?.max_instances ?? 1
  if (!target.value || isRunning(target.value) || max === 0) return []
  const running = state.models.filter((m) => isRunning(m))
  return running.length >= max ? running.map((m) => m.id) : []
})

async function run() {
  if (!model.value) return
  running.value = true
  error.value = ''
  runStart.value = Date.now()
  const t = window.setInterval(() => (elapsed.value = Math.round((Date.now() - runStart.value) / 1000)), 500)
  try {
    const r = await api.value!.bench(model.value, promptTokens.value, genTokens.value, restore.value)
    if (r.result.error) error.value = r.result.error
    await load()
  } catch (e: any) {
    error.value = e.message
  } finally {
    window.clearInterval(t)
    running.value = false
    refreshModels()
  }
}

async function remove(id: number) {
  await api.value!.deleteBench(id)
  list.value = list.value.filter((b) => b.id !== id)
}

// short summary of the settings that matter for speed
function config(preset: string) {
  const p = presetValues(preset)
  const parts = [`ctx ${fmtCtx(p['ctx-size'])}`]
  parts.push(p['device'] ? p['device'] : p['split-mode'] === 'none' ? 'main GPU' : `split ${p['split-mode'] ?? 'layer'} ${p['tensor-split'] ?? ''}`.trim())
  if (p['cache-type-k'] || p['cache-type-v']) parts.push(`KV ${p['cache-type-k'] ?? 'f16'}/${p['cache-type-v'] ?? 'f16'}`)
  if (p['flash-attn']) parts.push(`fa ${p['flash-attn']}`)
  if (p['n-gpu-layers'] && p['n-gpu-layers'] !== '999') parts.push(`ngl ${p['n-gpu-layers']}`)
  if (p['parallel']) parts.push(`np ${p['parallel']}`)
  if (p['spec-type'] && p['spec-type'] !== 'none') parts.push(`spec ${p['spec-type']}${p['spec-draft-n-max'] ? ` ×${p['spec-draft-n-max']}` : ''}`)
  return parts.join(' · ')
}

const rows = computed(() => {
  const f = filter.value.toLowerCase()
  return list.value.filter((b) => !f || `${b.result.model} ${llamaVersion(b.result.build)}`.toLowerCase().includes(f))
})
// best generation speed per model (for highlighting)
const best = computed(() => {
  const m: Record<string, number> = {}
  for (const b of list.value) if (!b.result.error) m[b.result.model] = Math.max(m[b.result.model] ?? 0, b.result.gen_tps)
  return m
})
const fmtTime = (s: string) => new Date(s).toLocaleString()
</script>

<template>
  <div class="space-y-4">
    <!-- live GPUs and host: watch memory, utilisation, power and temperature while a test runs -->
    <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
      <GpuCard
        v-for="(g, i) in state.gpus"
        :key="g.index"
        :gpu="g"
        :history="state.gpuHistory[g.index] ?? []"
        :device="state.status?.devices?.[i]"
      />
      <HostCard v-if="state.hostStats" :host="state.hostStats" :history="state.cpuHistory" />
    </div>

    <PowerSweep />

    <!-- speed test, right above its results -->
    <div class="card p-4">
      <div class="mb-3 font-semibold">Speed test</div>
      <div class="grid gap-3 sm:grid-cols-[1fr_auto_auto_auto] sm:items-end">
        <div class="min-w-0">
          <label class="label">Model / preset</label>
          <select v-model="model" class="input font-mono text-xs">
            <option v-for="m in state.models" :key="m.id" :value="m.id">{{ m.id }}{{ isRunning(m) ? ' (loaded)' : '' }}</option>
          </select>
        </div>
        <div>
          <label class="label">Prompt</label>
          <select v-model.number="promptTokens" class="input">
            <option :value="512">512 tokens</option>
            <option :value="2048">2K tokens</option>
            <option :value="8192">8K tokens</option>
            <option :value="16384">16K tokens</option>
            <option :value="32768">32K tokens</option>
          </select>
        </div>
        <div>
          <label class="label">Generate</label>
          <select v-model.number="genTokens" class="input">
            <option :value="64">64 tokens</option>
            <option :value="128">128 tokens</option>
            <option :value="512">512 tokens</option>
          </select>
        </div>
        <button class="btn btn-primary h-9" :disabled="running || !model" @click="run">
          {{ running ? `Running… ${elapsed}s` : 'Run test' }}
        </button>
      </div>
      <label class="mt-3 flex items-center gap-2 text-sm">
        <input v-model="restore" type="checkbox" /> Reload the currently loaded model afterwards
      </label>
      <p v-if="willUnload.length" class="mt-2 text-xs text-amber-700 dark:text-amber-300">
        Loading this model unloads {{ willUnload.join(', ') }} (models-max {{ state.status?.router.props?.max_instances }}); requests to it wait until it is back.
      </p>
      <p class="muted mt-2 text-xs">
        Uses llama.cpp's own timings: prompt cache off, end-of-text ignored so exactly the chosen number of tokens is generated, greedy sampling.
        GPU memory, temperature and power are sampled during the run.
      </p>
      <div v-if="error" class="mt-3 rounded-lg bg-red-50 px-3 py-2 text-sm text-red-700 dark:bg-red-950/50 dark:text-red-300">{{ error }}</div>
    </div>

    <div class="card">
      <div class="card-h flex-wrap">
        <span>Results <span class="muted font-normal">({{ rows.length }})</span></span>
        <input v-model="filter" class="input min-w-0 !py-1 text-xs font-normal sm:!w-56" placeholder="Filter by model or version…" />
      </div>
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="text-left text-xs text-zinc-500">
            <tr class="border-b border-zinc-200 dark:border-zinc-800">
              <th class="px-4 py-2 font-medium">Model &amp; config</th>
              <th class="px-3 py-2 text-right font-medium" title="Generation speed">Gen t/s</th>
              <th class="px-3 py-2 text-right font-medium" title="Prompt processing speed">Prompt t/s</th>
              <th class="hidden px-3 py-2 text-right font-medium sm:table-cell" title="Time to first token for the prompt">TTFT</th>
              <th class="hidden px-3 py-2 text-right font-medium md:table-cell">Load</th>
              <th class="hidden px-3 py-2 font-medium lg:table-cell">GPUs (peak · max °C · avg W while generating)</th>
              <th class="hidden px-3 py-2 font-medium xl:table-cell" title="Peak host RAM in use and llama.cpp's share; CPU while generating">Host (peak RAM · CPU)</th>
              <th class="px-2 py-2" />
            </tr>
          </thead>
          <tbody>
            <tr v-for="b in rows" :key="b.id" class="border-b border-zinc-100 align-top last:border-0 dark:border-zinc-800/60">
              <td class="px-4 py-2">
                <div class="font-mono text-xs font-medium break-all">{{ b.result.model }}</div>
                <div class="muted text-xs">{{ config(b.result.preset) }}</div>
                <div class="muted text-[11px]">
                  {{ fmtTime(b.result.time) }} · {{ b.result.prompt_n }} → {{ b.result.gen_n }} tokens
                  <template v-if="b.result.build"> · <span :title="`llama.cpp ${b.result.build}`">llama.cpp {{ llamaVersion(b.result.build) }}</span></template>
                </div>
                <div v-if="b.result.error" class="text-xs text-red-600">{{ b.result.error }}</div>
              </td>
              <td class="px-3 py-2 text-right tabular-nums">
                <span v-if="!b.result.error" class="font-semibold" :class="{ 'text-emerald-600 dark:text-emerald-400': b.result.gen_tps === best[b.result.model] }">
                  {{ b.result.gen_tps.toFixed(1) }}
                </span>
              </td>
              <td class="px-3 py-2 text-right tabular-nums">{{ b.result.error ? '' : Math.round(b.result.prompt_tps) }}</td>
              <td class="hidden px-3 py-2 text-right tabular-nums sm:table-cell">{{ b.result.error ? '' : (b.result.ttft_ms / 1000).toFixed(2) + 's' }}</td>
              <td class="hidden px-3 py-2 text-right tabular-nums md:table-cell">{{ b.result.load_ms ? (b.result.load_ms / 1000).toFixed(1) + 's' : '–' }}</td>
              <td class="hidden px-3 py-2 text-xs tabular-nums lg:table-cell">
                <div v-for="g in b.result.gpus ?? []" :key="g.index">
                  {{ state.status?.devices?.[g.index] ?? `CUDA${g.index}` }}: {{ (g.peak_mib / 1024).toFixed(1) }} GiB · {{ g.max_temp_c }}°C · {{ Math.round(g.avg_power_w) }} W
                  <span class="muted">(limit {{ Math.round(g.power_limit_w) }})</span>
                </div>
              </td>
              <td class="hidden px-3 py-2 text-xs tabular-nums xl:table-cell">
                <template v-if="b.result.host">
                  <div>
                    {{ (b.result.host.peak_mem_used / 2 ** 30).toFixed(1) }} / {{ (b.result.host.mem_total / 2 ** 30).toFixed(0) }} GiB
                    <span v-if="b.result.host.peak_unit_mem" class="muted">(llama.cpp {{ (b.result.host.peak_unit_mem / 2 ** 30).toFixed(1) }})</span>
                  </div>
                  <div>CPU avg {{ Math.round(b.result.host.avg_cpu_pct) }}% · max {{ Math.round(b.result.host.max_cpu_pct) }}%</div>
                  <div v-if="(b.result.host.peak_swap_used ?? 0) > 64 * 2 ** 20" class="text-amber-600 dark:text-amber-400">
                    swap {{ ((b.result.host.peak_swap_used ?? 0) / 2 ** 30).toFixed(1) }} GiB: paging may have slowed this run
                  </div>
                </template>
                <span v-else class="muted">–</span>
              </td>
              <td class="px-2 py-2 text-right">
                <button class="btn btn-sm" title="Delete result" @click="remove(b.id)">✕</button>
              </td>
            </tr>
            <tr v-if="!rows.length">
              <td colspan="8" class="muted px-4 py-8 text-center">No results yet. Run a test above.</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>
