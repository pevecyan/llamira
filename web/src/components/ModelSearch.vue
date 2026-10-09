<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import type { HFDetail, HFRepo, Quant } from '../api'
import { api, downloadProgress, fmtBytes, fmtCtx, refreshModels, state } from '../state'

// preview: size of the download under the pointer (for the storage bar), 0 when none
const emit = defineEmits<{ preview: [bytes: number] }>()

const route = useRoute()
const router = useRouter()

const query = ref((route.query.q as string) ?? '')
const results = ref<HFRepo[] | null>(null)
const searching = ref(false)
const detail = ref<HFDetail | null>(null)
const loadingRepo = ref(false)
const error = ref('')
const busy = ref<Record<string, boolean>>({})
const detailEl = ref<HTMLElement | null>(null)

async function search() {
  const q = query.value.trim()
  if (!q) return
  searching.value = true
  error.value = ''
  router.replace({ query: { ...route.query, q } })
  try {
    results.value = await api.value!.hfSearch(q)
  } catch (e: any) {
    error.value = e.message
  } finally {
    searching.value = false
  }
}

async function openRepo(id: string) {
  loadingRepo.value = true
  error.value = ''
  router.replace({ query: { ...route.query, repo: id } })
  try {
    detail.value = await api.value!.hfRepo(id)
    // on narrow screens the details are below the result list
    if (window.innerWidth < 1024) setTimeout(() => detailEl.value?.scrollIntoView({ behavior: 'smooth', block: 'start' }), 50)
  } catch (e: any) {
    error.value = e.message
    detail.value = null
  } finally {
    loadingRepo.value = false
  }
}

onMounted(() => {
  if (query.value) search()
  if (route.query.repo) openRepo(route.query.repo as string)
})

const downloads = computed(() => state.models.filter((m) => m.status.value === 'downloading'))
const vramTotal = computed(() => state.gpus.reduce((a, g) => a + g.mem_total_mib, 0) * 1024 * 1024)
const vramOne = computed(() => (state.gpus.length ? Math.max(...state.gpus.map((g) => g.mem_total_mib)) * 1024 * 1024 : 0))

// Rough guidance from the weight size only; the KV cache for the context comes on top.
function fit(q: Quant) {
  const need = q.size + q.extra_size + 0.6 * 2 ** 30 // weights + projector + runtime overhead
  if (!vramTotal.value) return null
  if (need < vramOne.value * 0.85) return { label: '1 GPU', cls: 'text-emerald-600 dark:text-emerald-400' }
  if (need < vramTotal.value * 0.85) return { label: `${state.gpus.length} GPUs`, cls: 'text-sky-600 dark:text-sky-400' }
  return { label: 'too big', cls: 'text-red-600 dark:text-red-400' }
}

function modelState(q: Quant) {
  const m = state.models.find((x) => x.id === q.ref)
  if (!m) return { kind: 'none' as const }
  const p = downloadProgress(m)
  if (p) return { kind: 'downloading' as const, p }
  return { kind: 'local' as const, status: m.status.value }
}

async function download(q: Quant) {
  const total = q.size + q.extra_size
  const free = state.local?.disk?.free
  let msg = `Download ${q.ref}?\n\n${fmtBytes(q.size)} model`
  if (q.extra_size) msg += ` + ${fmtBytes(q.extra_size)} (${q.extras.join(', ')})`
  if (free !== undefined) msg += `\n${fmtBytes(free)} free on ${state.local?.disk?.path}`
  if (free !== undefined && total > free * 0.95) {
    alert(`Not enough disk space: ${fmtBytes(total)} needed, ${fmtBytes(free)} free.`)
    return
  }
  if (!confirm(msg)) return
  busy.value[q.ref] = true
  error.value = ''
  try {
    await api.value!.download(q.ref)
    await refreshModels()
  } catch (e: any) {
    error.value = `${q.ref}: ${e.message}`
  } finally {
    busy.value[q.ref] = false
  }
}

async function cancel(id: string) {
  if (!confirm(`Cancel the download of ${id}?`)) return
  try {
    await api.value!.unload(id)
  } catch (e: any) {
    error.value = e.message
  } finally {
    setTimeout(refreshModels, 800)
  }
}

const fmtNum = (n?: number) =>
  n === undefined ? '–' : n >= 1e9 ? `${(n / 1e9).toFixed(1)}B` : n >= 1e6 ? `${(n / 1e6).toFixed(1)}M` : n >= 1e3 ? `${(n / 1e3).toFixed(1)}k` : String(n)
const fmtDate = (s?: string) => (s ? new Date(s).toLocaleDateString() : '')

watch(
  () => route.query.repo,
  (r) => {
    if (typeof r === 'string' && r !== detail.value?.id) openRepo(r)
  },
)
</script>

<template>
  <div class="space-y-4">
    <!-- active downloads -->
    <div v-if="downloads.length" class="card">
      <div class="card-h">Downloads <span class="muted font-normal">({{ downloads.length }})</span></div>
      <div v-for="m in downloads" :key="m.id" class="flex flex-wrap items-center gap-3 border-b border-zinc-100 px-4 py-2.5 text-sm last:border-0 dark:border-zinc-800/60">
        <span class="min-w-0 flex-1 truncate font-mono text-xs">{{ m.id }}</span>
        <template v-if="downloadProgress(m)">
          <div class="h-2 w-48 overflow-hidden rounded-full bg-zinc-200 dark:bg-zinc-800">
            <div class="h-full bg-emerald-500 transition-all" :style="{ width: (downloadProgress(m)!.pct * 100).toFixed(1) + '%' }" />
          </div>
          <span class="muted w-40 text-right text-xs tabular-nums">
            {{ fmtBytes(downloadProgress(m)!.done) }} / {{ fmtBytes(downloadProgress(m)!.total) }}
          </span>
        </template>
        <button class="btn btn-sm" @click="cancel(m.id)">Cancel</button>
      </div>
    </div>

    <form class="flex gap-2" @submit.prevent="search">
      <input v-model="query" class="input max-w-xl" placeholder="Search Hugging Face GGUF models, e.g. qwen3.5 9b, gemma 3, unsloth…" autofocus />
      <button class="btn btn-primary" :disabled="searching || !query.trim()">{{ searching ? 'Searching…' : 'Search' }}</button>
    </form>
    <div v-if="error" class="rounded-lg bg-red-50 px-4 py-2 text-sm text-red-700 dark:bg-red-950/50 dark:text-red-300">{{ error }}</div>

    <div class="grid gap-4 lg:grid-cols-[minmax(280px,380px)_1fr]">
      <!-- results -->
      <aside class="card h-fit max-h-[45vh] overflow-auto lg:max-h-[75vh]">
        <div v-if="!results" class="muted px-4 py-8 text-center text-sm">Search for a model to get started.</div>
        <div v-else-if="!results.length" class="muted px-4 py-8 text-center text-sm">No GGUF repositories found.</div>
        <button
          v-for="r in results"
          :key="r.id"
          class="block w-full border-b border-zinc-100 px-4 py-2.5 text-left last:border-0 hover:bg-zinc-50 dark:border-zinc-800/60 dark:hover:bg-zinc-800/40"
          :class="{ 'bg-zinc-100 dark:bg-zinc-800': detail?.id === r.id }"
          @click="openRepo(r.id)"
        >
          <div class="text-sm font-medium break-all">{{ r.id }}</div>
          <div class="muted mt-0.5 flex flex-wrap gap-x-3 text-xs">
            <span>↓ {{ fmtNum(r.downloads) }}</span>
            <span>♥ {{ fmtNum(r.likes) }}</span>
            <span v-if="r.pipeline_tag">{{ r.pipeline_tag }}</span>
          </div>
        </button>
      </aside>

      <!-- repo detail -->
      <section ref="detailEl" class="card min-w-0 scroll-mt-16">
        <div v-if="loadingRepo" class="muted px-4 py-10 text-center text-sm">Loading repository…</div>
        <div v-else-if="!detail" class="muted px-4 py-10 text-center text-sm">Select a repository to see its quantizations.</div>
        <template v-else>
          <div class="border-b border-zinc-200 p-4 dark:border-zinc-800">
            <div class="flex flex-wrap items-start justify-between gap-2">
              <a :href="detail.url" target="_blank" rel="noopener" class="font-semibold break-all hover:underline">{{ detail.id }} ↗</a>
              <span v-if="detail.gated" class="badge bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300">gated</span>
            </div>
            <div class="muted mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs">
              <span v-if="detail.params">{{ fmtNum(detail.params) }} params</span>
              <span v-if="detail.architecture">{{ detail.architecture }}</span>
              <span v-if="detail.context_length">ctx {{ fmtCtx(detail.context_length) }}</span>
              <span v-if="detail.license">{{ detail.license }}</span>
              <span v-if="detail.pipeline_tag">{{ detail.pipeline_tag }}</span>
              <span>↓ {{ fmtNum(detail.downloads) }}</span>
              <span>updated {{ fmtDate(detail.last_modified) }}</span>
              <span v-if="detail.base_model">base: {{ detail.base_model }}</span>
            </div>
            <p v-if="detail.gated" class="mt-2 text-xs text-amber-700 dark:text-amber-300">
              Gated repository: the router needs an HF token with access (HF_TOKEN in its environment) to download it.
            </p>
          </div>
          <div class="overflow-x-auto">
            <table class="w-full text-sm">
              <thead class="text-left text-xs text-zinc-500">
                <tr class="border-b border-zinc-200 dark:border-zinc-800">
                  <th class="px-2 py-2 font-medium sm:px-4">Quant</th>
                  <th class="px-2 py-2 font-medium sm:px-4">Size</th>
                  <th class="hidden px-4 py-2 font-medium sm:table-cell" title="Weights only: the KV cache for the context comes on top. Open the preset editor after downloading for a full estimate.">Fits (weights)</th>
                  <th class="px-2 py-2 sm:px-4" />
                </tr>
              </thead>
              <tbody>
                <tr v-for="q in detail.quants" :key="q.ref + q.files[0]?.path" class="border-b border-zinc-100 last:border-0 dark:border-zinc-800/60">
                  <td class="px-2 py-2 sm:px-4">
                    <span class="font-mono font-medium">{{ q.tag }}</span>
                    <span v-if="q.files.length > 1" class="muted ml-1.5 text-xs">{{ q.files.length }} parts</span>
                    <div v-if="q.dir" class="muted font-mono text-[11px]">{{ q.dir }}/</div>
                    <div v-if="q.ambiguous && !q.selectable" class="text-[11px] text-amber-600">shadowed by another {{ q.tag }} file</div>
                  </td>
                  <td class="px-2 py-2 whitespace-nowrap tabular-nums sm:px-4">
                    {{ fmtBytes(q.size) }}
                    <div v-if="q.extra_size" class="muted text-[11px]" :title="q.extras.join(', ')">+ {{ fmtBytes(q.extra_size) }} {{ q.extras.some((x) => x.includes('mmproj')) ? 'vision' : 'extra' }}</div>
                    <div v-if="fit(q)" class="text-[11px] sm:hidden" :class="fit(q)!.cls">{{ fit(q)!.label }}</div>
                  </td>
                  <td class="hidden px-4 py-2 text-xs sm:table-cell">
                    <span v-if="fit(q)" :class="fit(q)!.cls">{{ fit(q)!.label }}</span>
                  </td>
                  <td class="px-2 py-2 text-right sm:px-4">
                    <template v-if="modelState(q).kind === 'downloading'">
                      <span class="text-xs tabular-nums text-amber-600">{{ ((modelState(q) as any).p.pct * 100).toFixed(0) }}%</span>
                    </template>
                    <span v-else-if="modelState(q).kind === 'local'" class="badge bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300">
                      downloaded
                    </span>
                    <button
                      v-else
                      class="btn btn-sm btn-primary"
                      :disabled="!q.selectable || busy[q.ref]"
                      :title="q.selectable ? `Download ${q.ref}` : 'The router cannot address this file by its tag'"
                      @click="download(q)"
                      @mouseenter="emit('preview', q.size + q.extra_size)"
                      @mouseleave="emit('preview', 0)"
                    >
                      {{ busy[q.ref] ? 'Starting…' : 'Download' }}
                    </button>
                  </td>
                </tr>
                <tr v-if="!detail.quants.length">
                  <td colspan="4" class="muted px-4 py-6 text-center">No GGUF model files in this repository.</td>
                </tr>
              </tbody>
            </table>
          </div>
          <p class="muted border-t border-zinc-200 px-4 py-2 text-xs dark:border-zinc-800">
            Downloads run inside llama-server into its cache ({{ state.local?.cache_dir ?? 'LLAMA_CACHE' }}); a vision projector (mmproj) is fetched
            automatically. The model appears in the model list as <span class="font-mono">org/repo:QUANT</span> when finished.
          </p>
        </template>
      </section>
    </div>
  </div>
</template>
