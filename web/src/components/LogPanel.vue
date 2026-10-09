<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { api } from '../state'

const lines = ref<string[]>([])
const filter = ref('')
const paused = ref(false)
const connected = ref(false)
const box = ref<HTMLElement | null>(null)
let es: EventSource | null = null
const MAX = 2000

function open() {
  es?.close()
  if (!api.value) return
  lines.value = []
  es = api.value.logStream(300)
  es.onopen = () => (connected.value = true)
  es.onerror = () => (connected.value = false)
  es.addEventListener('log', (e) => {
    if (paused.value) return
    lines.value.push(JSON.parse((e as MessageEvent).data))
    if (lines.value.length > MAX) lines.value.splice(0, lines.value.length - MAX)
  })
}

onMounted(open)
onBeforeUnmount(() => es?.close())

const shown = computed(() => {
  const f = filter.value.toLowerCase()
  const src = f ? lines.value.filter((l) => l.toLowerCase().includes(f)) : lines.value
  return src
    .slice(-500)
    .map((l) => {
      // "2026-10-08T21:18:50+00:00 llama llama-server[532]: [40029] ... I slot ..."
      const m = l.match(/^(\S+)\s+\S+\s+\S+?:\s(?:\[(\d+)\]\s?)?(.*)$/)
      if (!m) return { time: '', port: '', msg: l, level: '' }
      const msg = m[3]
      const level = / E |error/i.test(msg) ? 'error' : / W |warn/i.test(msg) ? 'warn' : ''
      return { time: m[1].slice(11, 19), port: m[2] ?? '', msg, level }
    })
    .filter((l) => l.msg.trim() !== '')
})

watch(
  () => shown.value.length,
  async () => {
    const el = box.value
    if (!el) return
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40
    await nextTick()
    if (atBottom) el.scrollTop = el.scrollHeight
  },
)
</script>

<template>
  <div class="card flex flex-col">
    <div class="card-h flex-wrap">
      <span class="flex items-center gap-2">
        Router log
        <span class="h-1.5 w-1.5 rounded-full" :class="connected ? 'bg-emerald-500' : 'bg-zinc-400'" />
      </span>
      <div class="flex min-w-0 flex-1 items-center justify-end gap-2 font-normal">
        <input v-model="filter" class="input min-w-0 !py-1 text-xs sm:!w-44" placeholder="Filter…" />
        <button class="btn btn-sm" @click="paused = !paused">{{ paused ? 'Resume' : 'Pause' }}</button>
        <button class="btn btn-sm" @click="open">Reload</button>
      </div>
    </div>
    <div ref="box" class="h-80 overflow-auto bg-zinc-950 px-3 py-2 font-mono text-[11.5px] leading-[1.55] text-zinc-300 rounded-b-xl">
      <div v-for="(l, i) in shown" :key="i" class="whitespace-pre-wrap break-all">
        <span class="text-zinc-500">{{ l.time }}</span>
        <span v-if="l.port" class="ml-1.5 text-sky-400">[{{ l.port }}]</span>
        <span class="ml-1.5" :class="{ 'text-red-400': l.level === 'error', 'text-amber-300': l.level === 'warn' }">{{ l.msg }}</span>
      </div>
      <div v-if="!shown.length" class="text-zinc-500">No log lines.</div>
    </div>
  </div>
</template>
