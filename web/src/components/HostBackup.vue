<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, state } from '../state'

// Export this host's setup as one JSON file, or import one: schedules, alert settings and power
// limits are written directly; presets are loaded into the working copy for review & apply.
const router = useRouter()
const secrets = ref(false)
const error = ref('')
const result = ref<{ imported: string[]; errors: string[] } | null>(null)
const bundle = ref<Record<string, any> | null>(null)
const fileName = ref('')
const parts = ref<string[]>(['presets', 'schedules', 'alerts', 'power'])
const fileInput = ref<HTMLInputElement | null>(null)
const busy = ref(false)

async function doExport() {
  error.value = ''
  try {
    const b = await api.value!.exportBundle(secrets.value)
    const a = document.createElement('a')
    a.href = URL.createObjectURL(new Blob([JSON.stringify(b, null, 2)], { type: 'application/json' }))
    a.download = `llamira-${state.host}-${new Date().toISOString().slice(0, 10)}.json`
    a.click()
    URL.revokeObjectURL(a.href)
  } catch (e: any) {
    error.value = e.message
  }
}

async function pick(ev: Event) {
  const f = (ev.target as HTMLInputElement).files?.[0]
  ;(ev.target as HTMLInputElement).value = ''
  if (!f) return
  error.value = ''
  result.value = null
  try {
    const b = JSON.parse(await f.text())
    if (b.llamira_export !== 1) throw new Error('not a llamira export')
    bundle.value = b
    fileName.value = f.name
  } catch (e: any) {
    error.value = `${f.name}: ${e.message}`
  }
}

async function doImport() {
  const b = bundle.value
  if (!b) return
  const direct = parts.value.filter((p) => p !== 'presets')
  if (direct.length && !confirm(`Replace ${direct.join(', ')} on ${state.host} with the export from ${b.host}?`)) return
  busy.value = true
  error.value = ''
  try {
    result.value = direct.length ? await api.value!.importBundle(b, direct) : { imported: [], errors: [] }
    if (parts.value.includes('presets') && b.presets) {
      state.pending = { text: b.presets, label: `the presets exported from ${b.host}` }
      router.push('/presets')
    }
    bundle.value = null
  } catch (e: any) {
    error.value = e.message
  } finally {
    busy.value = false
  }
}
const PARTS = [
  { key: 'presets', label: 'Presets (opens the working copy for review & apply)' },
  { key: 'schedules', label: 'Schedules' },
  { key: 'alerts', label: 'Alert settings' },
  { key: 'power', label: 'GPU power limits' },
]
</script>

<template>
  <div class="card p-4 text-sm">
    <div class="mb-1 font-semibold">Backup &amp; restore</div>
    <p class="muted mb-3 text-xs">
      One JSON file with the preset file, schedules, alert settings, power limits and the llamira config (for reference; it is not imported).
      Models are not included: presets refer to them by id. Same as <code>llamira export</code> / <code>llamira import</code>.
    </p>
    <div class="flex flex-wrap items-center gap-2">
      <button class="btn btn-sm" @click="doExport">Export…</button>
      <label class="flex items-center gap-1.5 text-xs" title="Alert channel tokens, auth tokens, HF token and router API key">
        <input v-model="secrets" type="checkbox" /> include secrets
      </label>
      <button class="btn btn-sm ml-auto" @click="fileInput?.click()">Import…</button>
      <input ref="fileInput" type="file" accept=".json,application/json" class="hidden" @change="pick" />
    </div>
    <div v-if="bundle" class="mt-3 space-y-2 rounded-lg border border-zinc-200 p-3 dark:border-zinc-800">
      <div class="text-xs">
        <b>{{ fileName }}</b>: export of <b>{{ bundle.host }}</b> from {{ new Date(bundle.time).toLocaleString() }}
        · {{ (bundle.schedules ?? []).length }} schedules · {{ Object.keys(bundle.power_limits ?? {}).length }} power limits
        <template v-if="!bundle.secrets"> · secrets were left out (this host's are kept)</template>
      </div>
      <label v-for="p in PARTS" :key="p.key" class="flex items-center gap-2 text-xs">
        <input v-model="parts" type="checkbox" :value="p.key" /> {{ p.label }}
      </label>
      <div class="flex gap-2">
        <button class="btn btn-sm btn-primary" :disabled="busy || !parts.length" @click="doImport">{{ busy ? 'Importing…' : 'Import' }}</button>
        <button class="btn btn-sm" @click="bundle = null">Cancel</button>
      </div>
    </div>
    <div v-if="error" class="mt-2 text-xs text-red-600">{{ error }}</div>
    <ul v-if="result" class="mt-2 space-y-0.5 text-xs">
      <li v-for="d in result.imported" :key="d" class="text-emerald-700 dark:text-emerald-400">✓ {{ d }}</li>
      <li v-for="e in result.errors" :key="e" class="text-red-600">✗ {{ e }}</li>
    </ul>
  </div>
</template>
