<script setup lang="ts">
import { computed } from 'vue'
import type { VramEstimate } from '../api'
import { fmtCtx } from '../state'

const props = defineProps<{ estimate: VramEstimate | null; model?: string; error?: string; loading?: boolean; ram?: { available: number; total: number } }>()

const gib = (n: number) => (n / 2 ** 30).toFixed(n >= 10 * 2 ** 30 ? 1 : 2)
const mib = (n: number) => `${Math.round(n / 2 ** 20)} MiB`

const verdict = computed(() => {
  switch (props.estimate?.status) {
    case 'over':
      return { label: "won't fit", cls: 'bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300' }
    case 'tight':
      return { label: 'tight', cls: 'bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300' }
    default:
      return { label: 'fits', cls: 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300' }
  }
})

const parts = [
  { key: 'weights', label: 'weights', cls: 'bg-emerald-500' },
  { key: 'mmproj', label: 'vision', cls: 'bg-violet-500' },
  { key: 'kv', label: 'KV cache / state', cls: 'bg-sky-500' },
  { key: 'overhead', label: 'runtime', cls: 'bg-zinc-400 dark:bg-zinc-500' },
] as const

const kvTotal = computed(() => (props.estimate ? props.estimate.devices.reduce((a, d) => a + d.kv, 0) + props.estimate.cpu.kv : 0))
</script>

<template>
  <div class="rounded-lg border border-zinc-200 p-3 dark:border-zinc-800">
    <div class="mb-2 flex items-center justify-between gap-2">
      <span class="text-xs font-medium text-zinc-600 dark:text-zinc-400">
        VRAM estimate <span class="muted font-normal" title="Calibrated against nvidia-smi on this host; typically within ±5%">(±5%)</span>
      </span>
      <span v-if="loading" class="muted text-xs">calculating…</span>
      <span v-else-if="estimate" class="badge" :class="verdict.cls">{{ verdict.label }}</span>
    </div>
    <p v-if="error" class="muted text-xs">{{ error }}</p>
    <template v-else-if="estimate">
      <div v-for="d in estimate.devices" :key="d.name" class="mb-2.5">
        <div class="mb-1 flex justify-between text-xs tabular-nums">
          <span class="font-medium">{{ d.name }} <span class="muted font-normal">· {{ d.layers }} layers</span></span>
          <span :class="{ 'text-red-600 dark:text-red-400': d.status === 'over', 'text-amber-600': d.status === 'tight' }">
            {{ gib(d.total) }} / {{ gib(d.capacity || 0) }} GiB
          </span>
        </div>
        <div class="relative flex h-3 overflow-hidden rounded-full bg-zinc-200 dark:bg-zinc-800">
          <div
            v-for="p in parts"
            :key="p.key"
            :class="p.cls"
            :style="{ width: Math.min(100, (d[p.key] / Math.max(d.capacity || d.total, d.total)) * 100) + '%' }"
            :title="`${p.label}: ${mib(d[p.key])}`"
          />
        </div>
      </div>
      <div class="mb-2 flex flex-wrap gap-x-3 gap-y-1 text-[11px] text-zinc-500">
        <span v-for="p in parts" :key="p.key" class="flex items-center gap-1"><span class="h-2 w-2 rounded-sm" :class="p.cls" />{{ p.label }}</span>
      </div>
      <div class="muted space-y-0.5 text-xs">
        <div>
          Context {{ fmtCtx(estimate.ctx) }}<template v-if="estimate.ctx_train"> (trained {{ fmtCtx(estimate.ctx_train) }})</template> ·
          KV {{ gib(kvTotal) }} GiB
          <template v-if="estimate.kv_per_token">({{ Math.round(estimate.kv_per_token / 1024) }} KiB/token, {{ estimate.kv_layers }} of {{ estimate.layers }} layers)</template>
          <template v-if="estimate.recurrent_layers"> · {{ estimate.recurrent_layers }} recurrent layers × {{ estimate.slots }} slots</template>
          <template v-if="estimate.swa_layers"> · {{ estimate.swa_layers }} sliding-window layers</template>
        </div>
        <div v-if="estimate.cpu.total" :class="ram && estimate.cpu.total > ram.available ? 'text-red-600 dark:text-red-400' : ''">
          CPU RAM: {{ gib(estimate.cpu.total) }} GiB (token embeddings<template v-if="estimate.gpu_layers < estimate.layers">, offloaded layers</template>)<template v-if="ram">
            of about {{ gib(ram.available) }} GiB available to llama.cpp</template>
        </div>
      </div>
      <ul v-if="estimate.warnings.length" class="mt-2 space-y-1">
        <li v-for="w in estimate.warnings" :key="w" class="rounded bg-amber-50 px-2 py-1 text-xs text-amber-900 dark:bg-amber-950/50 dark:text-amber-200">{{ w }}</li>
      </ul>
      <ul v-if="estimate.notes.length" class="muted mt-2 list-disc space-y-0.5 pl-4 text-[11px]">
        <li v-for="n in estimate.notes" :key="n">{{ n }}</li>
      </ul>
    </template>
  </div>
</template>
