<script setup lang="ts">
import { computed } from 'vue'
import type { DiffLine } from '../api'

const props = defineProps<{ diff: DiffLine[]; context?: number }>()

// Collapse long runs of unchanged lines.
const blocks = computed(() => {
  const ctx = props.context ?? 3
  const d = props.diff
  const keep = d.map(() => false)
  d.forEach((l, i) => {
    if (l.op !== ' ') for (let k = Math.max(0, i - ctx); k <= Math.min(d.length - 1, i + ctx); k++) keep[k] = true
  })
  const out: ({ kind: 'line'; line: DiffLine } | { kind: 'gap'; n: number })[] = []
  let gap = 0
  d.forEach((l, i) => {
    if (keep[i]) {
      if (gap) out.push({ kind: 'gap', n: gap })
      gap = 0
      out.push({ kind: 'line', line: l })
    } else gap++
  })
  if (gap) out.push({ kind: 'gap', n: gap })
  return out
})
</script>

<template>
  <div class="overflow-auto rounded-lg border border-zinc-200 font-mono text-xs dark:border-zinc-800">
    <template v-for="(b, i) in blocks" :key="i">
      <div v-if="b.kind === 'gap'" class="bg-zinc-50 px-3 py-0.5 text-zinc-400 dark:bg-zinc-900">⋯ {{ b.n }} unchanged line{{ b.n > 1 ? 's' : '' }}</div>
      <div
        v-else
        class="whitespace-pre-wrap px-3 py-px"
        :class="{
          'bg-emerald-50 text-emerald-900 dark:bg-emerald-950/40 dark:text-emerald-200': b.line.op === '+',
          'bg-red-50 text-red-900 dark:bg-red-950/40 dark:text-red-200': b.line.op === '-',
        }"
      ><span class="select-none text-zinc-400">{{ b.line.op === ' ' ? ' ' : b.line.op }} </span>{{ b.line.text || ' ' }}</div>
    </template>
    <div v-if="!diff.length" class="muted px-3 py-2">Empty file.</div>
  </div>
</template>
