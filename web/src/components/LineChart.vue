<script setup lang="ts">
// Single-series line chart (one y axis; small multiples instead of dual axes).
import { computed, ref } from 'vue'

const props = defineProps<{
  title: string
  points: { x: number; y: number }[]
  unit?: string
  xUnit?: string
  zero?: boolean // start the y axis at 0 (magnitudes); otherwise fit the data
  marks?: { x: number; label: string }[]
  digits?: number
}>()

const W = 360
const H = 170
const PAD = { l: 44, r: 10, t: 10, b: 26 }
const pw = W - PAD.l - PAD.r
const ph = H - PAD.t - PAD.b

const xs = computed(() => props.points.map((p) => p.x))
const xMin = computed(() => Math.min(...xs.value))
const xMax = computed(() => Math.max(...xs.value))
const yr = computed(() => {
  const ys = props.points.map((p) => p.y)
  let lo = props.zero ? 0 : Math.min(...ys)
  let hi = Math.max(...ys)
  if (hi === lo) {
    hi += 1
    lo = props.zero ? 0 : lo - 1
  }
  const span = hi - lo
  const raw = span / 4
  const p = 10 ** Math.floor(Math.log10(raw))
  const step = [1, 2, 5, 10].map((f) => f * p).find((v) => v >= raw) ?? raw
  lo = props.zero ? 0 : Math.floor(lo / step) * step
  hi = Math.ceil(hi / step) * step
  return { lo, hi, step }
})
const ticks = computed(() => {
  const out: number[] = []
  for (let v = yr.value.lo; v <= yr.value.hi + 1e-9; v += yr.value.step) out.push(+v.toFixed(6))
  return out
})
const x = (v: number) => PAD.l + (xMax.value === xMin.value ? pw / 2 : ((v - xMin.value) / (xMax.value - xMin.value)) * pw)
const y = (v: number) => PAD.t + ph - ((v - yr.value.lo) / (yr.value.hi - yr.value.lo)) * ph
const path = computed(() => props.points.map((p, i) => `${i ? 'L' : 'M'}${x(p.x).toFixed(1)},${y(p.y).toFixed(1)}`).join(' '))
const fmt = (v: number) => v.toFixed(props.digits ?? (Math.abs(v) >= 100 ? 0 : 1))
const hover = ref<number | null>(null)
function onMove(e: MouseEvent) {
  const svg = e.currentTarget as SVGSVGElement
  const r = svg.getBoundingClientRect()
  const px = ((e.clientX - r.left) / r.width) * W
  let best = 0
  props.points.forEach((p, i) => {
    if (Math.abs(x(p.x) - px) < Math.abs(x(props.points[best].x) - px)) best = i
  })
  hover.value = props.points.length ? best : null
}
</script>

<template>
  <div class="relative">
    <div class="mb-1 text-xs font-semibold">{{ title }}</div>
    <svg :viewBox="`0 0 ${W} ${H}`" class="h-auto w-full" @mousemove="onMove" @mouseleave="hover = null">
      <g class="text-zinc-400 dark:text-zinc-500">
        <template v-for="t in ticks" :key="t">
          <line :x1="PAD.l" :x2="W - PAD.r" :y1="y(t)" :y2="y(t)" stroke="currentColor" stroke-opacity="0.25" />
          <text :x="PAD.l - 5" :y="y(t) + 3.5" text-anchor="end" font-size="10" fill="currentColor">{{ fmt(t) }}</text>
        </template>
        <text v-for="p in points" :key="'x' + p.x" :x="x(p.x)" :y="H - 8" text-anchor="middle" font-size="10" fill="currentColor">{{ p.x }}</text>
      </g>
      <line
        v-for="m in marks ?? []"
        :key="m.label"
        :x1="x(m.x)"
        :x2="x(m.x)"
        :y1="PAD.t"
        :y2="PAD.t + ph"
        stroke="var(--series-2)"
        stroke-dasharray="3 3"
        stroke-width="1"
      />
      <path v-if="points.length > 1" :d="path" fill="none" stroke="var(--series-1)" stroke-width="2" stroke-linejoin="round" />
      <circle
        v-for="(p, i) in points"
        :key="i"
        :cx="x(p.x)"
        :cy="y(p.y)"
        :r="hover === i ? 5 : 4"
        fill="var(--series-1)"
        stroke="var(--chart-surface)"
        stroke-width="2"
      />
      <line v-if="hover !== null" :x1="x(points[hover].x)" :x2="x(points[hover].x)" :y1="PAD.t" :y2="PAD.t + ph" stroke="currentColor" stroke-opacity="0.3" />
    </svg>
    <div
      v-if="hover !== null"
      class="pointer-events-none absolute top-5 rounded-md border border-zinc-200 bg-white px-2 py-1 text-xs shadow dark:border-zinc-700 dark:bg-zinc-900"
      :style="{ left: `min(calc(${(x(points[hover].x) / W) * 100}% + 8px), calc(100% - 7rem))` }"
    >
      <b>{{ points[hover].x }} {{ xUnit ?? 'W' }}</b>: {{ fmt(points[hover].y) }} {{ unit }}
      <div v-for="m in (marks ?? []).filter((m) => m.x === points[hover!].x)" :key="m.label" class="text-[10px] text-zinc-500">{{ m.label }}</div>
    </div>
  </div>
</template>
