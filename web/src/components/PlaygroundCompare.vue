<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { samplingBody, streamChat, type Reply, type Sampling } from '../chatstream'
import { onMarkdownClick, renderMarkdown } from '../markdown'
import { api, isRunning, refreshModels, state } from '../state'

// One prompt, two models, run one after the other (with models-max 1 the router swaps them).
const props = defineProps<{ system: string; temperature: number; maxTokens: number; sampling: Sampling }>()

const models = ref<[string, string]>(['', ''])
watch(
  () => state.models.length,
  () => {
    const ids = state.models.map((m) => m.id)
    if (!models.value[0]) models.value[0] = state.models.find((m) => isRunning(m))?.id ?? ids[0] ?? ''
    if (!models.value[1]) models.value[1] = ids.find((id) => id !== models.value[0]) ?? ''
  },
  { immediate: true },
)
const prompt = ref('')
const replies = reactive<[Reply | null, Reply | null]>([null, null])
const running = ref<number | null>(null)
let abort: AbortController | null = null

async function run() {
  const text = prompt.value.trim()
  if (!text || running.value !== null) return
  abort = new AbortController()
  const messages = [...(props.system.trim() ? [{ role: 'system', content: props.system.trim() }] : []), { role: 'user', content: text }]
  for (const i of [0, 1] as const) {
    replies[i] = { content: '' }
  }
  for (const i of [0, 1] as const) {
    if (abort.signal.aborted) break
    running.value = i
    const r = replies[i]!
    await streamChat(
      api.value!.base,
      { model: models.value[i], messages, temperature: props.temperature, max_tokens: props.maxTokens, ...samplingBody(props.sampling) },
      r,
      abort.signal,
    )
  }
  running.value = null
  abort = null
  refreshModels()
}

function stop() {
  abort?.abort()
}

function onKey(e: KeyboardEvent) {
  if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
    e.preventDefault()
    run()
  }
}
</script>

<template>
  <div class="card flex min-h-[420px] flex-col">
    <div class="space-y-2 border-b border-zinc-200 p-3 dark:border-zinc-800">
      <textarea v-model="prompt" rows="3" class="input resize-y" placeholder="Prompt for both models… (Ctrl+Enter to run)" @keydown="onKey" />
      <div class="flex flex-wrap items-center gap-2">
        <span class="muted text-xs">The models run one after the other, so a model that isn't loaded is loaded first (and may unload the other).</span>
        <button v-if="running !== null" class="btn ml-auto" @click="stop">Stop</button>
        <button v-else class="btn btn-primary ml-auto" :disabled="!prompt.trim() || !models[0] || !models[1]" @click="run">Run both</button>
      </div>
    </div>
    <div class="grid flex-1 gap-0 md:grid-cols-2">
      <div
        v-for="i in [0, 1] as const"
        :key="i"
        class="flex min-w-0 flex-col border-zinc-200 p-3 dark:border-zinc-800"
        :class="i === 0 ? 'border-b md:border-r md:border-b-0' : ''"
      >
        <select v-model="models[i]" class="input mb-2 font-mono text-xs" :disabled="running !== null">
          <option v-for="m in state.models" :key="m.id" :value="m.id">{{ m.id }}{{ isRunning(m) ? ' (loaded)' : '' }}</option>
        </select>
        <div v-if="!replies[i]" class="muted py-6 text-center text-xs">No reply yet.</div>
        <template v-else>
          <div v-if="running === i && !replies[i]!.content && !replies[i]!.reasoning" class="muted text-xs">waiting (loading the model if needed)…</div>
          <div v-else-if="running !== null && running < i" class="muted text-xs">queued…</div>
          <details v-if="replies[i]!.reasoning" class="mb-2 text-xs">
            <summary class="cursor-pointer text-zinc-500">thinking ({{ replies[i]!.reasoning!.length }} chars)</summary>
            <div class="mt-1 whitespace-pre-wrap text-zinc-500">{{ replies[i]!.reasoning }}</div>
          </details>
          <div class="md min-w-0" @click="onMarkdownClick" v-html="renderMarkdown(replies[i]!.content)" />
          <span v-if="running === i" class="animate-pulse">▍</span>
          <div v-if="replies[i]!.error" class="mt-1 text-xs text-red-600 dark:text-red-400">{{ replies[i]!.error }}</div>
          <div v-if="replies[i]!.finish === 'length'" class="mt-1 text-xs text-amber-700 dark:text-amber-300">Stopped at the max-token limit.</div>
          <div v-if="replies[i]!.timings" class="mt-2 text-[11px] text-zinc-500 tabular-nums">
            {{ replies[i]!.timings!.prompt_n }} prompt tok @ {{ Math.round(replies[i]!.timings!.prompt_per_second) }} t/s ·
            {{ replies[i]!.timings!.predicted_n }} tok @ {{ replies[i]!.timings!.predicted_per_second.toFixed(1) }} t/s
          </div>
        </template>
      </div>
    </div>
  </div>
</template>
