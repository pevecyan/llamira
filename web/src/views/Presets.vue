<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import ApplyDialog from '../components/ApplyDialog.vue'
import DiffView from '../components/DiffView.vue'
import PresetForm from '../components/PresetForm.vue'
import type { ArgCatalog, KV, PresetDoc, PresetFile } from '../api'
import { api, refreshModels, state } from '../state'

const route = useRoute()
const router = useRouter()

const catalog = ref<ArgCatalog | null>(null)
const server = ref<PresetFile | null>(null)
const work = ref<PresetDoc | null>(null)
const selected = ref('*')
const draftName = ref<string | null>(null) // a section being created, not yet in the working copy
const draftEntries = ref<KV[]>([]) // prefilled entries for it (from the Models page)
const raw = ref(false)
const rawText = ref('')
const showApply = ref(false)
const showDiff = ref(false)
const error = ref('')
const notice = ref('')
const showBackups = ref(false)
const backups = ref<{ name: string; time: string; size: number }[]>([])
const fileInput = ref<HTMLInputElement | null>(null)
const editorEl = ref<HTMLElement | null>(null)

async function load() {
  error.value = ''
  try {
    const [c, p] = await Promise.all([api.value!.args(), api.value!.presets()])
    catalog.value = c
    server.value = p
    work.value = { text: p.text, preamble: p.preamble, sections: p.sections }
    if (state.pending) {
      const { text, label } = state.pending
      state.pending = null
      await loadText(text, label)
    }
    const q = route.query.section as string | undefined
    if (q) select(q)
    if (typeof route.query.new === 'string') prefill(route.query.new, route.query.ctx as string, route.query.device as string, route.query.embed === '1')
  } catch (e: any) {
    error.value = e.message
  }
}
onMounted(load)

const dirty = computed(() => !!server.value && !!work.value && server.value.text !== work.value.text)
const modelIds = computed(() => new Set(state.models.map((m) => m.id)))
const sections = computed(() => work.value?.sections ?? [])
const sectionNames = computed(() => new Set(sections.value.map((s) => s.name)))

const canonMap = computed(() => {
  const m = new Map<string, string>()
  for (const a of catalog.value?.args ?? []) {
    for (const n of [...a.names, ...(a.neg_names ?? [])]) m.set(n, a.key)
    if (a.env) m.set(a.env, a.key)
  }
  return m
})
const inherited = computed(() => {
  const out: Record<string, string> = {}
  for (const kv of sections.value.find((s) => s.name === '*')?.entries ?? []) out[canonMap.value.get(kv.key) ?? kv.key] = kv.value
  return out
})

const presetSections = computed(() => sections.value.filter((s) => s.name !== '*'))
const uncustomized = computed(() => state.models.filter((m) => m.source === 'cache' && !sectionNames.value.has(m.id)))

const current = computed(() => {
  const name = draftName.value ?? selected.value
  const sec = sections.value.find((s) => s.name === name)
  return {
    name,
    entries: sec?.entries ?? (name === draftName.value ? draftEntries.value : []),
    isNew: !sec,
    isGlobal: name === '*',
    existingModel: state.models.find((m) => m.id === name && m.source !== 'preset'),
  }
})

function select(name: string) {
  draftEntries.value = []
  draftName.value = sectionNames.value.has(name) || name === '*' ? null : name
  selected.value = name
  raw.value = false
  router.replace({ query: name === '*' ? {} : { section: name } })
  // on narrow screens the editor is below the section list
  if (window.innerWidth < 1024 && editorEl.value) setTimeout(() => editorEl.value?.scrollIntoView({ behavior: 'smooth', block: 'start' }), 50)
}

// new preset for a downloaded model, e.g. "unsloth/Qwen3.5-9B-GGUF:Q4_K_M" → [qwen3.5-9b-q4_k_m]
function prefill(id: string, ctx?: string, device?: string, embed = false) {
  const [repo, tag] = id.split(':')
  let base = (repo.split('/').pop() ?? repo).replace(/-GGUF$/i, '').toLowerCase()
  if (tag) base += '-' + tag.toLowerCase()
  let n = base
  for (let i = 2; sectionNames.value.has(n); i++) n = `${base}-${i}`
  const entries: KV[] = [{ key: 'hf-repo', value: id }]
  if (ctx) entries.push({ key: 'ctx-size', value: ctx })
  if (device) entries.push({ key: 'device', value: device })
  if (embed) entries.push({ key: 'embeddings', value: 'true' })
  select(n)
  draftEntries.value = entries
}

// A variant: the same settings under a new name. An override of a cached model becomes a
// custom preset pointing at that model. Aliases stay with the original (they must be unique).
function duplicate(entries: KV[]) {
  const src = current.value
  const out = entries.filter((e) => e.key !== 'alias')
  if (!out.some((e) => e.key === 'hf-repo' || e.key === 'model')) out.unshift({ key: 'hf-repo', value: src.name })
  let base = src.name
  if (src.existingModel) {
    const [repo, tag] = src.name.split(':')
    base = (repo.split('/').pop() ?? repo).replace(/-GGUF$/i, '').toLowerCase() + (tag ? '-' + tag.toLowerCase() : '')
  }
  base = base.replace(/-v\d+$/, '')
  let n = `${base}-v2`
  for (let i = 3; sectionNames.value.has(n) || n === src.name; i++) n = `${base}-v${i}`
  select(n)
  draftEntries.value = out
  notice.value = `New variant [${n}] of [${src.name}]: change what you want to compare, then save it.`
}

function newPreset() {
  let n = 'new-preset'
  for (let i = 2; sectionNames.value.has(n); i++) n = `new-preset-${i}`
  select(n)
}

async function edit(op: 'upsert' | 'delete' | 'rename', section: string, extra: { entries?: KV[]; new_name?: string } = {}) {
  error.value = ''
  try {
    work.value = await api.value!.edit({ text: work.value!.text, op, section, ...extra })
    return true
  } catch (e: any) {
    error.value = e.message
    return false
  }
}

async function onSave(entries: KV[]) {
  const name = current.value.name
  if (await edit('upsert', name, { entries })) {
    draftName.value = null
    selected.value = name
  }
}

async function onDelete() {
  const name = current.value.name
  if (!confirm(`Remove [${name}] from the working copy?`)) return
  if (await edit('delete', name)) select('*')
}

async function onRename(newName: string) {
  if (current.value.isNew) {
    draftName.value = newName
    selected.value = newName
    return
  }
  if (await edit('rename', current.value.name, { new_name: newName })) select(newName)
}

function openRaw() {
  rawText.value = work.value?.text ?? ''
  raw.value = true
}

async function useRaw() {
  error.value = ''
  try {
    work.value = await api.value!.parse(rawText.value)
    raw.value = false
  } catch (e: any) {
    error.value = e.message
  }
}

// Replace the working copy with another version of the file; it is applied through the
// normal review → dry-run → apply → verify flow.
async function loadText(text: string, label: string) {
  error.value = ''
  try {
    work.value = await api.value!.parse(text)
    notice.value =
      server.value && text === server.value.text
        ? `${label} is identical to the current file.`
        : `Loaded ${label} into the working copy. Review & apply to make it live.`
    select('*')
  } catch (e: any) {
    error.value = e.message
  }
}

async function toggleBackups() {
  showBackups.value = !showBackups.value
  if (showBackups.value) {
    try {
      backups.value = await api.value!.backups()
    } catch (e: any) {
      error.value = e.message
    }
  }
}

async function loadBackup(name: string) {
  try {
    const b = await api.value!.backup(name)
    await loadText(b.text, `backup ${name.replace('models.ini.', '')}`)
  } catch (e: any) {
    error.value = e.message
  }
}

async function importFile(ev: Event) {
  const f = (ev.target as HTMLInputElement).files?.[0]
  if (!f) return
  if (f.size > 1 << 20) {
    error.value = 'File too large for a preset file'
    return
  }
  await loadText(await f.text(), `file ${f.name}`)
  ;(ev.target as HTMLInputElement).value = ''
}

function exportFile() {
  if (!work.value) return
  const a = document.createElement('a')
  a.href = URL.createObjectURL(new Blob([work.value.text], { type: 'text/plain' }))
  a.download = `models-${state.host}-${new Date().toISOString().slice(0, 10)}.ini`
  a.click()
  URL.revokeObjectURL(a.href)
}

function discard() {
  if (!server.value || !confirm('Discard all unapplied changes?')) return
  work.value = { text: server.value.text, preamble: server.value.preamble, sections: server.value.sections }
  select('*')
}

async function onApplied() {
  const name = selected.value
  await load()
  refreshModels()
  if (name) select(name)
}

const workingDiff = computed(() => {
  // cheap client-side line diff for the "pending changes" peek; the server diff is authoritative
  if (!server.value || !work.value) return []
  const a = server.value.text.split('\n')
  const b = work.value.text.split('\n')
  const n = a.length
  const m = b.length
  const L: number[][] = Array.from({ length: n + 1 }, () => new Array(m + 1).fill(0))
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) L[i][j] = a[i] === b[j] ? L[i + 1][j + 1] + 1 : Math.max(L[i + 1][j], L[i][j + 1])
  const out: { op: ' ' | '+' | '-'; text: string }[] = []
  let i = 0
  let j = 0
  while (i < n && j < m) {
    if (a[i] === b[j]) (out.push({ op: ' ', text: a[i] }), i++, j++)
    else if (L[i + 1][j] >= L[i][j + 1]) out.push({ op: '-', text: a[i++] })
    else out.push({ op: '+', text: b[j++] })
  }
  while (i < n) out.push({ op: '-', text: a[i++] })
  while (j < m) out.push({ op: '+', text: b[j++] })
  return out
})

watch(
  () => route.query.section,
  (q) => {
    if (typeof q === 'string' && q !== (draftName.value ?? selected.value)) select(q)
  },
)
</script>

<template>
  <div v-if="!catalog || !work" class="muted py-20 text-center">
    <span v-if="error" class="text-red-600">{{ error }}</span>
    <span v-else>Loading presets…</span>
  </div>
  <div v-else class="space-y-4">
    <!-- pending changes bar -->
    <div
      v-if="dirty"
      class="sticky top-14 z-10 lg:top-2 flex flex-wrap items-center gap-3 rounded-xl border border-amber-300 bg-amber-50 px-4 py-2.5 text-sm shadow-sm dark:border-amber-800 dark:bg-amber-950/60"
    >
      <span class="font-medium text-amber-900 dark:text-amber-200">Unapplied changes in the working copy</span>
      <button class="btn btn-sm" @click="showDiff = !showDiff">{{ showDiff ? 'Hide' : 'Show' }} diff</button>
      <div class="ml-auto flex gap-2">
        <button class="btn btn-sm" @click="discard">Discard</button>
        <button class="btn btn-sm btn-primary" @click="showApply = true">Review &amp; apply</button>
      </div>
      <DiffView v-if="showDiff" :diff="workingDiff" class="w-full bg-white dark:bg-zinc-900" />
    </div>
    <div v-if="error" class="rounded-lg bg-red-50 px-4 py-2 text-sm text-red-700 dark:bg-red-950/50 dark:text-red-300">{{ error }}</div>
    <div v-if="notice" class="flex items-center gap-3 rounded-lg bg-sky-50 px-4 py-2 text-sm text-sky-900 dark:bg-sky-950/50 dark:text-sky-200">
      {{ notice }}
      <button class="ml-auto text-xs underline" @click="notice = ''">dismiss</button>
    </div>

    <div class="grid gap-4 lg:grid-cols-[280px_1fr]">
      <!-- section list -->
      <aside class="card h-fit p-2 text-sm">
        <button
          class="w-full rounded-lg px-3 py-2 text-left hover:bg-zinc-100 dark:hover:bg-zinc-800"
          :class="{ 'bg-zinc-100 font-semibold dark:bg-zinc-800': current.name === '*' && !raw }"
          @click="select('*')"
        >
          Global defaults <span class="muted font-mono text-xs">[*]</span>
        </button>

        <div class="mt-3 mb-1 flex items-center justify-between px-3 text-xs font-medium uppercase tracking-wide text-zinc-500">
          Presets
          <button class="btn btn-sm !py-0.5 normal-case" @click="newPreset">+ New</button>
        </div>
        <button
          v-for="s in presetSections"
          :key="s.name"
          class="w-full rounded-lg px-3 py-1.5 text-left hover:bg-zinc-100 dark:hover:bg-zinc-800"
          :class="{ 'bg-zinc-100 font-semibold dark:bg-zinc-800': current.name === s.name && !raw }"
          @click="select(s.name)"
        >
          <div class="truncate font-mono text-xs" :title="s.name">{{ s.name }}</div>
          <div class="muted text-[11px]">
            {{ modelIds.has(s.name) && !s.entries.some((e) => e.key === 'hf-repo' || e.key === 'model') ? 'overrides cached model' : 'custom preset' }}
            · {{ s.entries.length }} option{{ s.entries.length === 1 ? '' : 's' }}
          </div>
        </button>
        <div v-if="draftName" class="rounded-lg bg-emerald-50 px-3 py-1.5 font-mono text-xs dark:bg-emerald-950/40">{{ draftName }} <span class="muted">(unsaved)</span></div>
        <p v-if="!presetSections.length && !draftName" class="muted px-3 py-1 text-xs">No presets yet.</p>

        <template v-if="uncustomized.length">
          <div class="mt-3 mb-1 px-3 text-xs font-medium uppercase tracking-wide text-zinc-500">Cached models (defaults only)</div>
          <button
            v-for="m in uncustomized"
            :key="m.id"
            class="w-full truncate rounded-lg px-3 py-1.5 text-left font-mono text-xs text-zinc-600 hover:bg-zinc-100 dark:text-zinc-400 dark:hover:bg-zinc-800"
            :title="`Customise ${m.id}`"
            @click="select(m.id)"
          >
            {{ m.id }}
          </button>
        </template>

        <div class="mt-3 border-t border-zinc-200 pt-2 dark:border-zinc-800">
          <button
            class="w-full rounded-lg px-3 py-2 text-left hover:bg-zinc-100 dark:hover:bg-zinc-800"
            :class="{ 'bg-zinc-100 font-semibold dark:bg-zinc-800': raw }"
            @click="openRaw"
          >
            Raw file editor
          </button>
          <div class="muted px-3 pb-1 font-mono text-[11px] break-all">{{ server?.path }}</div>
          <div class="flex flex-wrap gap-1.5 px-3 py-2">
            <button class="btn btn-sm" title="Download the working copy" @click="exportFile">Export</button>
            <button class="btn btn-sm" title="Load a models.ini into the working copy" @click="fileInput?.click()">Import…</button>
            <button class="btn btn-sm" @click="toggleBackups">Backups</button>
            <input ref="fileInput" type="file" accept=".ini,text/plain" class="hidden" @change="importFile" />
          </div>
          <div v-if="showBackups" class="max-h-64 overflow-auto px-1 pb-1">
            <p v-if="!backups.length" class="muted px-2 py-1 text-xs">No backups yet. One is made before every apply.</p>
            <button
              v-for="b in backups"
              :key="b.name"
              class="flex w-full justify-between rounded-lg px-2 py-1 text-left text-xs hover:bg-zinc-100 dark:hover:bg-zinc-800"
              :title="`Load ${b.name} into the working copy`"
              @click="loadBackup(b.name)"
            >
              <span>{{ new Date(b.time).toLocaleString() }}</span>
              <span class="muted">{{ b.size }} B</span>
            </button>
          </div>
        </div>
      </aside>

      <!-- editor -->
      <section ref="editorEl" class="card scroll-mt-16 p-4 sm:p-5">
        <template v-if="raw">
          <div class="mb-2 flex items-center justify-between">
            <div class="font-semibold">Raw preset file</div>
            <div class="flex gap-2">
              <button class="btn btn-sm" @click="raw = false">Cancel</button>
              <button class="btn btn-sm btn-primary" @click="useRaw">Use this text</button>
            </div>
          </div>
          <textarea v-model="rawText" spellcheck="false" class="input h-[60vh] font-mono text-xs leading-relaxed" />
          <p class="muted mt-2 text-xs">Edits only the working copy. Comments and unknown entries are kept as you write them.</p>
        </template>
        <PresetForm
          v-else
          :name="current.name"
          :entries="current.entries"
          :catalog="catalog"
          :is-global="current.isGlobal"
          :is-new="current.isNew"
          :existing-model="current.existingModel"
          :inherited="inherited"
          :models="state.models"
          :devices="state.status?.devices ?? state.gpus.map((g) => `CUDA${g.index}`)"
          :text="work.text"
          @save="onSave"
          @delete="onDelete"
          @rename="onRename"
          @duplicate="duplicate"
        />
      </section>
    </div>

    <ApplyDialog
      v-if="showApply && work && server"
      :text="work.text"
      :base-hash="server.hash"
      @close="showApply = false"
      @applied="onApplied"
    />
  </div>
</template>
