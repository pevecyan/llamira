<script setup lang="ts">
import { computed } from 'vue'
import type { HostStats } from '../api'

// Host RAM (llama.cpp / other / free), swap, CPU with a sparkline, load and CPU temperature.
const props = defineProps<{ host: HostStats; history: number[] }>()

const gib = (b: number) => (b / 2 ** 30).toFixed(b >= 10 * 2 ** 30 ? 0 : 1)
const ram = computed(() => {
  const h = props.host
  const total = h.mem_total || 1
  // llama.cpp: the unit's anon + file-backed pages (mmap'd weights); other: the rest in use
  const llama = Math.min(h.unit?.mem ?? 0, total)
  const other = Math.max(0, Math.min(total - llama, h.mem_used - (h.unit?.anon ?? 0)))
  const free = Math.max(0, total - llama - other)
  return { llama, other, free, total, pct: (x: number) => (x / total) * 100 }
})
const usedPct = computed(() => (props.host.mem_used / (props.host.mem_total || 1)) * 100)
const swapping = computed(() => props.host.swap_used > 64 * 2 ** 20)
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
        <div class="text-xs font-medium uppercase tracking-wide text-zinc-500">Host</div>
        <div class="font-semibold">{{ host.cores }} cores · {{ gib(host.mem_total) }} GiB RAM</div>
      </div>
      <div class="text-right text-xs text-zinc-500 tabular-nums">
        <div v-if="host.cpu_temp_c">{{ Math.round(host.cpu_temp_c) }} °C</div>
        <div title="load average 1 / 5 / 15 min">load {{ host.load[0].toFixed(2) }}</div>
      </div>
    </div>
    <div class="mt-3">
      <div class="mb-1 flex justify-between text-xs tabular-nums">
        <span class="text-zinc-500">RAM</span>
        <span
          :title="`In use, counting llama.cpp's mmap'd weights. The kernel counts ${gib(host.mem_used)} GiB as used: the weights are page cache it could drop (and would then read from disk again).`"
          >{{ gib(ram.llama + ram.other) }} / {{ gib(host.mem_total) }} GiB</span
        >
      </div>
      <div class="flex h-2 overflow-hidden rounded-full bg-zinc-200 dark:bg-zinc-800" :class="usedPct > 92 ? 'ring-1 ring-red-500' : ''">
        <div class="h-full bg-emerald-500 transition-all duration-500" :style="{ width: ram.pct(ram.llama) + '%' }" :title="`llama.cpp: ${gib(ram.llama)} GiB`" />
        <div class="h-full bg-zinc-400 transition-all duration-500 dark:bg-zinc-500" :style="{ width: ram.pct(ram.other) + '%' }" :title="`other: ${gib(ram.other)} GiB`" />
      </div>
      <div class="mt-1 flex flex-wrap gap-x-3 text-[11px] text-zinc-500 tabular-nums">
        <span v-if="host.unit" class="flex items-center gap-1" :title="`${host.unit.name} (${host.unit.source}): ${gib(host.unit.anon)} GiB anonymous (KV cache, buffers), ${gib(host.unit.file)} GiB file-backed (mmap'd weights)`">
          <span class="h-2 w-2 rounded-sm bg-emerald-500" />llama.cpp {{ gib(host.unit.mem) }}
        </span>
        <span class="flex items-center gap-1"><span class="h-2 w-2 rounded-sm bg-zinc-400 dark:bg-zinc-500" />other {{ gib(ram.other) }}</span>
        <span :title="`MemAvailable: ${gib(host.mem_available)} GiB, including page cache such as mmap'd weights`">free {{ gib(ram.free) }}</span>
      </div>
      <div v-if="host.swap_total" class="mt-1 text-[11px] tabular-nums" :class="swapping ? 'font-medium text-amber-600 dark:text-amber-400' : 'text-zinc-500'">
        swap {{ gib(host.swap_used) }} / {{ gib(host.swap_total) }} GiB<template v-if="swapping"> · in use: requests may be slow</template>
      </div>
    </div>
    <div class="mt-3 flex items-end justify-between gap-3">
      <div>
        <div class="text-xs whitespace-nowrap text-zinc-500" :title="host.unit ? 'llama.cpp: router + model processes, as a share of all cores' : undefined">
          CPU<template v-if="host.unit"> ({{ Math.round(host.unit.cpu_pct) }}% llama)</template>
        </div>
        <div class="text-xl font-semibold tabular-nums">{{ Math.round(host.cpu_pct) }}%</div>
      </div>
      <svg viewBox="0 0 120 30" class="h-8 w-32 text-sky-500" preserveAspectRatio="none">
        <polyline :points="spark" fill="none" stroke="currentColor" stroke-width="1.5" vector-effect="non-scaling-stroke" />
      </svg>
    </div>
  </div>
</template>
