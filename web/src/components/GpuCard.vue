<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { GPU, GPUPower, Sweep } from '../api'
import { api } from '../state'

const props = defineProps<{ gpu: GPU; history: number[]; power?: GPUPower; device?: string }>()
const emit = defineEmits<{ changed: [] }>()

// power limit editor
const editing = ref(false)
const watts = ref(0)
const busy = ref(false)
const err = ref('')
watch(
  () => props.power?.limit_w,
  (w) => (watts.value = Math.round(w ?? 0)),
  { immediate: true },
)
// The latest finished power sweep that covered this GPU, if any, supplies measured presets.
const sweep = ref<Sweep | null>(null)
watch(editing, async (open) => {
  if (!open || sweep.value) return
  try {
    const r = await api.value!.sweep()
    sweep.value =
      r.saved
        .filter((s) => s.status === 'done' && s.picks && s.request.gpus.includes(props.gpu.index))
        .sort((a, b) => b.started.localeCompare(a.started))[0] ?? null
  } catch {
    /* fall back to the default-based presets */
  }
})
const presets = computed(() => {
  const p = props.power
  if (!p) return []
  const clamp = (w: number) => Math.round(Math.min(p.max_w, Math.max(p.min_w, w)) / 5) * 5
  const picks = sweep.value?.picks
  if (picks) {
    return [
      { label: 'Quiet', w: clamp(picks.efficient.watts), title: picks.efficient.reason },
      { label: 'Balanced', w: clamp(picks.balanced.watts), title: picks.balanced.reason },
      { label: 'Max', w: clamp(picks.max.watts), title: picks.max.reason },
    ]
  }
  return [
    { label: 'Quiet', w: clamp(p.default_w * 0.7), title: '70% of the default limit' },
    { label: 'Balanced', w: clamp(p.default_w * 0.85), title: '85% of the default limit' },
    { label: 'Max', w: Math.round(p.default_w), title: 'Default limit' },
  ]
})
async function applyPower(w: number) {
  busy.value = true
  err.value = ''
  try {
    await api.value!.setGpuPower(props.gpu.index, w)
    editing.value = false
    emit('changed')
  } catch (e: any) {
    err.value = e.message
  } finally {
    busy.value = false
  }
}

const memPct = computed(() => (props.gpu.mem_total_mib ? (props.gpu.mem_used_mib / props.gpu.mem_total_mib) * 100 : 0))
const memColor = computed(() => (memPct.value > 92 ? 'bg-red-500' : memPct.value > 75 ? 'bg-amber-500' : 'bg-emerald-500'))
const spark = computed(() => {
  const h = props.history.length ? props.history : [0]
  const w = 120
  const step = w / Math.max(h.length - 1, 1)
  return h.map((v, i) => `${(i * step).toFixed(1)},${(28 - (v / 100) * 26).toFixed(1)}`).join(' ')
})
</script>

<template>
  <div class="card p-4">
    <div class="flex items-start justify-between gap-2">
      <div>
        <div class="text-xs font-medium tracking-wide text-zinc-500">{{ device ?? `CUDA${gpu.index}` }}</div>
        <div class="font-semibold">{{ gpu.name.replace('NVIDIA GeForce ', '') }}</div>
      </div>
      <div class="text-right text-xs text-zinc-500 tabular-nums">
        <div>{{ gpu.temp_c }} °C</div>
        <div>{{ gpu.power_w.toFixed(0) }} / {{ gpu.power_limit_w.toFixed(0) }} W</div>
      </div>
    </div>
    <div class="mt-3">
      <div class="mb-1 flex justify-between text-xs tabular-nums">
        <span class="text-zinc-500">VRAM</span>
        <span>{{ (gpu.mem_used_mib / 1024).toFixed(1) }} / {{ (gpu.mem_total_mib / 1024).toFixed(1) }} GiB</span>
      </div>
      <div class="h-2 overflow-hidden rounded-full bg-zinc-200 dark:bg-zinc-800">
        <div class="h-full rounded-full transition-all duration-500" :class="memColor" :style="{ width: memPct + '%' }" />
      </div>
    </div>
    <div class="mt-3 flex items-end justify-between gap-3">
      <div>
        <div class="text-xs text-zinc-500">Utilisation</div>
        <div class="text-xl font-semibold tabular-nums">{{ gpu.util_pct }}%</div>
      </div>
      <svg viewBox="0 0 120 30" class="h-8 w-32 text-emerald-500" preserveAspectRatio="none">
        <polyline :points="spark" fill="none" stroke="currentColor" stroke-width="1.5" vector-effect="non-scaling-stroke" />
      </svg>
    </div>
    <div v-if="power" class="mt-3 border-t border-zinc-100 pt-2 text-xs dark:border-zinc-800">
      <div class="flex items-center justify-between">
        <span class="muted">
          Power limit {{ Math.round(power.limit_w) }} W<template v-if="power.limit_w !== power.default_w"> (default {{ Math.round(power.default_w) }})</template>
        </span>
        <button class="text-zinc-500 underline-offset-2 hover:underline" @click="editing = !editing">{{ editing ? 'close' : 'change' }}</button>
      </div>
      <div v-if="editing" class="mt-2 space-y-2">
        <div class="flex gap-1.5">
          <button v-for="p in presets" :key="p.label" class="btn btn-sm flex-1" :title="p.title" :disabled="busy" @click="applyPower(p.w === Math.round(power.default_w) ? 0 : p.w)">
            {{ p.label }} {{ p.w }}W
          </button>
        </div>
        <div class="flex items-center gap-2">
          <input v-model.number="watts" type="range" :min="power.min_w" :max="power.max_w" step="5" class="min-w-0 flex-1 accent-emerald-600" />
          <span class="w-12 text-right tabular-nums">{{ watts }} W</span>
          <button class="btn btn-sm btn-primary" :disabled="busy" @click="applyPower(watts)">Set</button>
        </div>
        <p v-if="sweep" class="muted">Presets from the power sweep of {{ new Date(sweep.started).toLocaleDateString() }} ({{ sweep.request.model.split('/').pop() }}).</p>
        <p class="muted">Applied immediately and re-applied at boot. Lower limits usually cost little generation speed; run a power sweep on Benchmarks to measure.</p>
        <p v-if="err" class="text-red-600">{{ err }}</p>
      </div>
    </div>
  </div>
</template>
