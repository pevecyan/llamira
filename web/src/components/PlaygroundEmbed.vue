<script setup lang="ts">
import { computed, ref } from 'vue'
import { cosine, embed } from '../chatstream'
import { api, refreshModels } from '../state'

// Embed a few texts with an embedding model and compare them (cosine similarity).
const props = defineProps<{ model: string }>()

const texts = ref<string[]>(['The cat sits on the mat.', 'A kitten is resting on a rug.', 'Quarterly revenue grew by 12%.'])
const vectors = ref<number[][]>([])
const error = ref('')
const busy = ref(false)
const ms = ref(0)

async function run() {
  const input = texts.value.map((t) => t.trim()).filter(Boolean)
  if (input.length < 1 || busy.value) return
  busy.value = true
  error.value = ''
  vectors.value = []
  const t0 = performance.now()
  try {
    vectors.value = await embed(api.value!.base, props.model, input)
    ms.value = performance.now() - t0
  } catch (e: any) {
    error.value = e.message
  } finally {
    busy.value = false
    refreshModels()
  }
}

const used = computed(() => texts.value.map((t) => t.trim()).filter(Boolean))
const matrix = computed(() => vectors.value.map((a) => vectors.value.map((b) => cosine(a, b))))
const norm = computed(() => vectors.value.map((v) => Math.sqrt(v.reduce((s, x) => s + x * x, 0))))
// shade by similarity: off-diagonal cells only
function shade(v: number, i: number, j: number) {
  if (i === j) return {}
  const a = Math.max(0, Math.min(1, v))
  return { backgroundColor: `color-mix(in srgb, var(--series-3) ${Math.round(a * 70)}%, transparent)` }
}
</script>

<template>
  <div class="card space-y-3 p-4 text-sm">
    <p class="muted text-xs">
      Needs an embedding model (a preset with <code>embeddings = true</code>). The texts are sent in one
      <code>/v1/embeddings</code> request; the matrix shows the cosine similarity of every pair.
    </p>
    <div v-for="(_, i) in texts" :key="i" class="flex gap-2">
      <span class="muted w-5 shrink-0 pt-1.5 text-right text-xs tabular-nums">{{ i + 1 }}</span>
      <textarea v-model="texts[i]" rows="1" class="input resize-y" />
      <button class="btn btn-sm h-fit" title="Remove" :disabled="texts.length <= 1" @click="texts.splice(i, 1)">✕</button>
    </div>
    <div class="flex flex-wrap items-center gap-2">
      <button class="btn btn-sm" :disabled="texts.length >= 12" @click="texts.push('')">+ Text</button>
      <span v-if="!model" class="text-xs text-amber-700 dark:text-amber-300">Select a model on the right.</span>
      <button class="btn btn-primary ml-auto" :disabled="busy || !model || !used.length" @click="run">{{ busy ? 'Embedding…' : 'Embed' }}</button>
    </div>
    <div v-if="error" class="rounded-lg bg-red-50 px-3 py-2 text-xs text-red-700 dark:bg-red-950/50 dark:text-red-300">{{ error }}</div>
    <template v-if="vectors.length">
      <div class="muted text-xs tabular-nums">
        {{ vectors.length }} vector{{ vectors.length === 1 ? '' : 's' }} × {{ vectors[0].length }} dimensions · {{ Math.round(ms) }} ms ·
        norm {{ norm.map((n) => n.toFixed(3)).join(', ') }}
      </div>
      <div class="overflow-x-auto">
        <table class="text-xs tabular-nums">
          <thead>
            <tr>
              <th class="px-2 py-1" />
              <th v-for="(_, j) in vectors" :key="j" class="px-2 py-1 font-medium">{{ j + 1 }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(row, i) in matrix" :key="i">
              <th class="px-2 py-1 text-right font-medium" :title="used[i]">{{ i + 1 }}</th>
              <td v-for="(v, j) in row" :key="j" class="rounded px-2 py-1 text-center" :class="i === j ? 'muted' : ''" :style="shade(v, i, j)">
                {{ v.toFixed(3) }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
  </div>
</template>
