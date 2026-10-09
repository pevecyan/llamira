<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import LineChart from './LineChart.vue'
import type { GPUPower, Sweep } from '../api'
import { api, isRunning, presetValues, state } from '../state'

const power = ref<GPUPower[]>([])
const model = ref('')
const gpus = ref<number[]>([])
const minW = ref(100)
const maxW = ref(170)
const stepW = ref(10)
const promptTokens = ref(8192)
const genTokens = ref(1024)
const current = ref<Sweep | null>(null)
const saved = ref<Sweep[]>([])
const viewing = ref<Sweep | null>(null)
const error = ref('')
const notice = ref('')
let timer: number | undefined
const open = ref(false) // results expanded

async function load() {
  try {
    const r = await api.value!.sweep()
    current.value = r.current
    saved.value = r.saved
    if (!viewing.value || viewing.value.status === 'running' || viewing.value === current.value) viewing.value = r.current ?? r.saved[0] ?? null
  } catch (e: any) {
    error.value = e.message
  }
}

onMounted(async () => {
  try {
    power.value = await api.value!.gpuPower()
  } catch {
    /* no GPUs */
  }
  await load()
  timer = window.setInterval(() => {
    if (current.value?.status === 'running') load()
  }, 2000)
})
onBeforeUnmount(() => window.clearInterval(timer))

// defaults: the loaded model and the GPUs its preset uses
watch(
  () => state.models.length,
  () => {
    if (!model.value) model.value = state.models.find((m) => isRunning(m))?.id ?? state.models[0]?.id ?? ''
  },
  { immediate: true },
)
watch([model, power], () => {
  const m = state.models.find((x) => x.id === model.value)
  const dev = presetValues(m?.status.preset)['device']
  const all = power.value.map((p) => p.index)
  gpus.value = dev ? all.filter((i) => dev.split(',').some((d) => d.trim() === `CUDA${i}`)) : all
})
watch(
  [gpus, power],
  () => {
    const sel = power.value.filter((p) => gpus.value.includes(p.index))
    if (!sel.length) return
    minW.value = Math.max(...sel.map((p) => p.min_w))
    maxW.value = Math.min(...sel.map((p) => p.max_w))
  },
  { deep: true },
)

const plan = computed(() => {
  const out: number[] = []
  if (stepW.value <= 0 || maxW.value < minW.value) return out
  for (let w = minW.value; w <= maxW.value + 0.01; w += stepW.value) out.push(Math.round(w))
  if (out.length && out[out.length - 1] < maxW.value) out.push(maxW.value)
  return out
})
// rough duration: settle + prompt (~2500 t/s) + generation (~45 t/s) per step, plus load
const estMin = computed(() => Math.ceil((60 + plan.value.length * (5 + promptTokens.value / 2500 + genTokens.value / 45 + 3)) / 60))

async function start() {
  if (!confirm(`Run a power sweep over ${plan.value.length} steps (${plan.value[0]}–${plan.value[plan.value.length - 1]} W, ~${estMin.value} min)?\n\nThe model is loaded (other models may be unloaded) and the GPUs run under full load. Original limits are restored at the end.`)) return
  error.value = ''
  try {
    viewing.value = await api.value!.startSweep({
      model: model.value, gpus: gpus.value, min_w: minW.value, max_w: maxW.value, step_w: stepW.value,
      prompt_tokens: promptTokens.value, gen_tokens: genTokens.value,
    })
    current.value = viewing.value
  } catch (e: any) {
    error.value = e.message
  }
}

async function cancel() {
  await api.value!.cancelSweep()
  setTimeout(load, 1000)
}

async function applyPick(watts: number, label: string) {
  const sw = viewing.value
  if (!sw || !confirm(`Set ${sw.request.gpus.map((g) => `CUDA${g}`).join(' + ')} to ${watts} W (${label}) and keep it after reboot?`)) return
  try {
    for (const g of sw.request.gpus) await api.value!.setGpuPower(g, watts)
    notice.value = `Power limit set to ${watts} W.`
    power.value = await api.value!.gpuPower()
  } catch (e: any) {
    error.value = e.message
  }
}

const ok = computed(() => (viewing.value?.steps ?? []).filter((s) => !s.error && s.gen_tps > 0))
const series = (f: (s: Sweep['steps'][number]) => number) => ok.value.map((s) => ({ x: s.watts, y: f(s) }))
const marks = computed(() => {
  const p = viewing.value?.picks
  if (!p) return []
  return [
    { x: p.max.watts, label: 'max performance' },
    { x: p.balanced.watts, label: 'balanced' },
    { x: p.efficient.watts, label: 'efficient' },
  ]
})
const running = computed(() => current.value?.status === 'running')
watch(running, (r) => r && (open.value = true), { immediate: true })
</script>

<template>
  <div class="card">
    <div class="card-h">
      Power optimization
      <span class="muted text-xs font-normal">find the power limit where speed stops scaling</span>
    </div>
    <div class="space-y-4 p-4 text-sm">
      <div v-if="error" class="rounded-lg bg-red-50 px-3 py-2 text-red-700 dark:bg-red-950/50 dark:text-red-300">{{ error }}</div>
      <div v-if="notice" class="rounded-lg bg-emerald-50 px-3 py-2 text-emerald-800 dark:bg-emerald-950/50 dark:text-emerald-300">{{ notice }}</div>

      <!-- setup -->
      <div v-if="!running" class="grid gap-3 lg:grid-cols-[1fr_auto]">
        <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <div class="sm:col-span-2">
            <label class="label">Model / preset</label>
            <select v-model="model" class="input font-mono text-xs">
              <option v-for="m in state.models" :key="m.id" :value="m.id">{{ m.id }}</option>
            </select>
          </div>
          <div>
            <label class="label">GPUs (same limit on each)</label>
            <div class="flex gap-3 pt-1.5">
              <label v-for="p in power" :key="p.index" class="flex items-center gap-1.5">
                <input v-model="gpus" type="checkbox" :value="p.index" /> CUDA{{ p.index }}
              </label>
            </div>
          </div>
          <div class="grid grid-cols-3 gap-1.5">
            <div><label class="label">Min W</label><input v-model.number="minW" type="number" class="input" /></div>
            <div><label class="label">Max W</label><input v-model.number="maxW" type="number" class="input" /></div>
            <div><label class="label">Step</label><input v-model.number="stepW" type="number" min="5" class="input" /></div>
          </div>
        </div>
        <div class="flex flex-col justify-end">
          <button class="btn btn-primary" :disabled="!model || !gpus.length || !plan.length || plan.length > 40" @click="start">Start sweep</button>
        </div>
        <p class="muted text-xs lg:col-span-2">
          {{ plan.length }} steps ({{ plan.join(', ') }} W), each an {{ promptTokens / 1024 }}K-token prompt + {{ genTokens }} generated tokens: about {{ estMin }} min.
          Limits are set temporarily and the original ones are restored afterwards (also after a crash or cancel). Stops if a GPU reaches the alert temperature.
        </p>
      </div>

      <!-- progress / results: collapsed to one line (usually only needed once); open while running -->
      <div v-if="viewing && !open" class="flex flex-wrap items-center gap-2 rounded-lg border border-zinc-200 px-3 py-2 text-xs dark:border-zinc-800">
        <span class="badge" :class="viewing.status === 'done' ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300' : 'bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300'">{{ viewing.status }}</span>
        <span class="muted">Last sweep {{ new Date(viewing.started).toLocaleDateString() }} · {{ viewing.request.model.split('/').pop() }}</span>
        <span v-if="viewing.picks" class="tabular-nums">
          balanced <b>{{ viewing.picks.balanced.watts }} W</b> · quiet <b>{{ viewing.picks.efficient.watts }} W</b> · max <b>{{ viewing.picks.max.watts }} W</b>
        </span>
        <button class="btn btn-sm ml-auto" @click="open = true">Show results<template v-if="saved.length > 1"> ({{ saved.length }} sweeps)</template></button>
      </div>
      <template v-if="viewing && open">
        <div class="flex flex-wrap items-center gap-2">
          <span class="badge" :class="{
            'bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300': viewing.status === 'running',
            'bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300': viewing.status === 'done',
            'bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300': viewing.status === 'failed' || viewing.status === 'cancelled',
          }">{{ viewing.status }}</span>
          <span class="font-mono text-xs">{{ viewing.request.model }}</span>
          <span class="muted text-xs">
            {{ viewing.request.gpus.map((g) => `CUDA${g}`).join(' + ') }} · {{ viewing.steps.length }}/{{ viewing.plan.length }} steps ·
            {{ new Date(viewing.started).toLocaleString() }}<template v-if="viewing.build"> · {{ viewing.build }}</template>
          </span>
          <button v-if="viewing.status === 'running'" class="btn btn-sm ml-auto" @click="cancel">Cancel</button>
          <select v-if="saved.length && !running" class="input ml-auto !w-auto !py-1 text-xs" @change="viewing = saved[+($event.target as HTMLSelectElement).value]">
            <option v-for="(s, i) in saved" :key="s.id" :value="i" :selected="viewing.id === s.id">
              {{ new Date(s.started).toLocaleString() }} · {{ s.request.model.split('/').pop() }} · {{ s.status }}
            </option>
          </select>
          <button v-if="!running" class="btn btn-sm" @click="open = false">Hide</button>
        </div>
        <div v-if="viewing.status === 'running'" class="h-1.5 overflow-hidden rounded-full bg-zinc-200 dark:bg-zinc-800">
          <div class="h-full bg-amber-500 transition-all" :style="{ width: (viewing.steps.length / viewing.plan.length) * 100 + '%' }" />
        </div>
        <p v-if="viewing.error" class="text-xs text-red-600">{{ viewing.error }}</p>

        <div v-if="viewing.picks" class="grid gap-2 sm:grid-cols-3">
          <div v-for="k in ['balanced', 'efficient', 'max'] as const" :key="k" class="rounded-lg border p-3" :class="k === 'balanced' ? 'border-emerald-500' : 'border-zinc-200 dark:border-zinc-800'">
            <div class="flex items-baseline justify-between">
              <span class="font-semibold">{{ { balanced: 'Balanced', efficient: 'Efficient / quiet', max: 'Max performance' }[k] }}</span>
              <span class="text-lg font-semibold tabular-nums">{{ viewing.picks[k].watts }} W</span>
            </div>
            <p class="muted mt-1 text-xs">{{ viewing.picks[k].reason }}</p>
            <button class="btn btn-sm mt-2" @click="applyPick(viewing.picks[k].watts, k)">Apply</button>
          </div>
        </div>

        <div v-if="ok.length" class="grid gap-4 sm:grid-cols-2">
          <LineChart title="Generation (tokens/s)" unit="t/s" :points="series((s) => s.gen_tps)" :marks="marks" zero />
          <LineChart title="Prompt processing (tokens/s)" unit="t/s" :points="series((s) => s.prompt_tps)" :marks="marks" zero />
          <LineChart title="Max temperature (°C)" unit="°C" :points="series((s) => s.max_temp_c)" :marks="marks" :digits="0" />
          <LineChart title="Efficiency (generated tokens per joule)" unit="tok/J" :points="series((s) => s.tokens_per_joule)" :marks="marks" :digits="3" />
        </div>
        <div v-if="viewing.steps.length" class="overflow-x-auto">
          <table class="w-full text-xs tabular-nums">
            <thead class="text-left text-zinc-500">
              <tr class="border-b border-zinc-200 dark:border-zinc-800">
                <th class="py-1.5 pr-3 font-medium">Limit</th><th class="pr-3 text-right font-medium">Gen t/s</th><th class="pr-3 text-right font-medium">Prompt t/s</th>
                <th class="pr-3 text-right font-medium">Avg W</th><th class="pr-3 text-right font-medium">Max °C</th><th class="pr-3 text-right font-medium">Clock</th>
                <th class="pr-3 text-right font-medium">Fan</th><th class="text-right font-medium">tok/J</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="s in viewing.steps" :key="s.watts" class="border-b border-zinc-100 dark:border-zinc-800/60">
                <td class="py-1 pr-3 font-medium">{{ s.watts }} W</td>
                <template v-if="!s.error">
                  <td class="pr-3 text-right">{{ s.gen_tps.toFixed(1) }}</td><td class="pr-3 text-right">{{ Math.round(s.prompt_tps) }}</td>
                  <td class="pr-3 text-right">{{ Math.round(s.avg_power_w) }}</td><td class="pr-3 text-right">{{ s.max_temp_c }}</td>
                  <td class="pr-3 text-right">{{ Math.round(s.avg_clock_mhz) }} MHz</td><td class="pr-3 text-right">{{ s.max_fan_pct >= 0 ? s.max_fan_pct + '%' : '–' }}</td>
                  <td class="text-right">{{ s.tokens_per_joule.toFixed(3) }}</td>
                </template>
                <td v-else colspan="7" class="text-red-600">{{ s.error }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </div>
  </div>
</template>
