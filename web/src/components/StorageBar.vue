<script setup lang="ts">
import { computed } from 'vue'
import { fmtBytes, state } from '../state'

// Disk usage under the models directory: GGUF models, other used space, free space.
// pending previews a download as a striped segment taken from the free space.
const props = defineProps<{ compact?: boolean; pending?: number }>()

const disk = computed(() => state.local?.disk)
const models = computed(
  () => (state.local?.models ?? []).reduce((a, m) => a + m.size, 0) + (state.local?.dir_files ?? []).reduce((a, f) => a + f.size, 0),
)
// same threshold as the low-disk alert (red below it, amber below twice it)
const lowBytes = computed(() => (state.local?.disk_low_gb || 20) * 2 ** 30)
const level = computed(() => {
  const free = disk.value?.free ?? Infinity
  return free < lowBytes.value ? 'critical' : free < 2 * lowBytes.value ? 'warning' : 'ok'
})

const segs = computed(() => {
  const d = disk.value
  if (!d?.total) return []
  const m = Math.min(models.value, d.used)
  const pend = Math.min(props.pending ?? 0, d.free)
  const pct = (n: number) => (n / d.total) * 100
  return [
    { key: 'models', label: 'Models', bytes: m, pct: pct(m) },
    { key: 'other', label: 'Other', bytes: d.used - m, pct: pct(d.used - m) },
    { key: 'pending', label: 'This download', bytes: pend, pct: pct(pend) },
    { key: 'free', label: 'Free', bytes: d.free - pend, pct: pct(d.free - pend) },
  ].filter((s) => s.bytes > 0 || s.key === 'free')
})
const tooBig = computed(() => !!props.pending && !!disk.value && props.pending > disk.value.free * 0.95)

const fill: Record<string, string> = {
  models: 'bg-[var(--series-1)]',
  other: 'bg-zinc-400 dark:bg-zinc-500',
  free: 'bg-zinc-200 dark:bg-zinc-800',
}
const freeFill = computed(() =>
  level.value === 'critical' ? 'bg-red-200 dark:bg-red-950' : level.value === 'warning' ? 'bg-amber-200 dark:bg-amber-950' : fill.free,
)
const pct = (p: number) => (p < 1 && p > 0 ? '<1' : p.toFixed(0))
</script>

<template>
  <div v-if="disk" :class="compact ? 'w-40' : 'w-full'" :title="compact ? `${fmtBytes(disk.free)} free on ${disk.path}` : undefined">
    <div class="flex overflow-hidden rounded-full bg-[var(--chart-surface)]" :class="compact ? 'h-1.5 gap-px' : 'h-3 gap-0.5'">
      <div
        v-for="s in segs"
        :key="s.key"
        class="h-full first:rounded-l-full last:rounded-r-full transition-all duration-300"
        :class="s.key === 'pending' ? 'pending' : s.key === 'free' ? freeFill : fill[s.key]"
        :style="{ width: s.pct + '%', minWidth: s.bytes > 0 ? '3px' : '0' }"
        :title="`${s.label}: ${fmtBytes(s.bytes)} (${pct(s.pct)}%)`"
      />
    </div>
    <div v-if="!compact" class="mt-1.5 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs tabular-nums">
      <span v-for="s in segs" :key="s.key" class="flex items-center gap-1.5">
        <span
          class="inline-block h-2.5 w-2.5 rounded-sm"
          :class="s.key === 'pending' ? 'pending' : s.key === 'free' ? [freeFill, 'ring-1 ring-inset ring-zinc-300 dark:ring-zinc-600'] : fill[s.key]"
        />
        <span class="muted">{{ s.label }}</span> {{ fmtBytes(s.bytes) }} <span class="muted">({{ pct(s.pct) }}%)</span>
      </span>
      <span class="muted ml-auto">{{ fmtBytes(disk.total) }} on {{ disk.path }}</span>
      <span v-if="level !== 'ok'" class="w-full font-medium" :class="level === 'critical' ? 'text-red-600 dark:text-red-400' : 'text-amber-700 dark:text-amber-300'">
        ⚠ Low disk space: {{ fmtBytes(disk.free) }} free ({{ level === 'critical' ? 'below' : 'near' }} the {{ state.local?.disk_low_gb || 20 }} GB alert threshold)
      </span>
      <span v-if="tooBig" class="w-full font-medium text-red-600 dark:text-red-400">⚠ This download does not fit: {{ fmtBytes(pending!) }} needed.</span>
    </div>
  </div>
</template>

<style scoped>
.pending {
  background: repeating-linear-gradient(45deg, var(--series-1) 0 3px, transparent 3px 6px);
}
</style>
