<script setup lang="ts">
import { onMounted, ref } from 'vue'
import type { ApplyResult, Preview, VramEstimate } from '../api'
import { api } from '../state'
import DiffView from './DiffView.vue'

const props = defineProps<{ text: string; baseHash: string }>()
const emit = defineEmits<{ close: []; applied: [result: ApplyResult] }>()

const preview = ref<Preview | null>(null)
const ok = ref(false)
const loading = ref(true)
const applying = ref(false)
const result = ref<ApplyResult | null>(null)
const error = ref('')
const summary = ref('')
const showLog = ref(false)

// VRAM estimates for models whose configuration is added or changed (best effort).
const vram = ref<Record<string, VramEstimate>>({})

onMounted(async () => {
  try {
    const r = await api.value!.preview(props.text)
    preview.value = r.preview
    ok.value = r.ok
    for (const m of r.preview.models ?? []) {
      if (m.change === 'removed') continue
      api.value!.vram(props.text, m.id).then(
        (v) => (vram.value[m.id] = v.estimate),
        () => {}, // not downloaded / no local file: no estimate
      )
    }
  } catch (e: any) {
    error.value = e.message
  } finally {
    loading.value = false
  }
})

function vramLabel(e: VramEstimate) {
  const per = e.devices.map((d) => `${(d.total / 2 ** 30).toFixed(1)}/${((d.capacity ?? 0) / 2 ** 30).toFixed(0)} GiB ${d.name}`).join(', ')
  return `${e.status === 'over' ? "won't fit" : e.status === 'tight' ? 'tight' : 'fits'}: ${per}`
}

async function doApply() {
  applying.value = true
  error.value = ''
  try {
    result.value = await api.value!.apply(props.text, props.baseHash, summary.value || 'ui')
    if (result.value.ok) emit('applied', result.value)
  } catch (e: any) {
    error.value = e.status === 409 ? 'The preset file was changed by someone else. Close, reload and redo your change.' : e.message
  } finally {
    applying.value = false
  }
}

const unloads = () => preview.value?.models?.filter((m) => m.unloads) ?? []
</script>

<template>
  <div class="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/40 p-4 sm:p-10" @click.self="emit('close')">
    <div class="card w-full max-w-3xl shadow-xl">
      <div class="card-h text-base">
        Review &amp; apply
        <button class="btn btn-sm" @click="emit('close')">✕</button>
      </div>
      <div class="space-y-4 p-4">
        <div v-if="loading" class="muted py-6 text-center text-sm">Validating and test-starting a throwaway router…</div>
        <div v-if="error" class="rounded-lg bg-red-50 px-3 py-2 text-sm text-red-700 dark:bg-red-950/50 dark:text-red-300">{{ error }}</div>

        <template v-if="preview && !result">
          <div>
            <div class="label">Changes to the preset file</div>
            <DiffView v-if="preview.changed" :diff="preview.diff" class="max-h-80" />
            <p v-else class="muted text-sm">No changes.</p>
          </div>

          <div v-if="preview.issues.length" class="space-y-1">
            <div class="label">Validation</div>
            <div
              v-for="(i, n) in preview.issues"
              :key="n"
              class="rounded-md px-3 py-1.5 text-sm"
              :class="i.severity === 'error' ? 'bg-red-50 text-red-800 dark:bg-red-950/50 dark:text-red-300' : 'bg-amber-50 text-amber-800 dark:bg-amber-950/40 dark:text-amber-200'"
            >
              <span class="font-semibold">{{ i.severity }}</span>
              <span v-if="i.section" class="font-mono"> [{{ i.section }}]</span> {{ i.message }}
            </div>
          </div>

          <div class="flex items-center gap-2 text-sm">
            <span class="h-2 w-2 rounded-full" :class="preview.dry_run_ok ? 'bg-emerald-500' : 'bg-red-500'" />
            <span v-if="preview.dry_run_ok">Throwaway router accepted the file.</span>
            <span v-else>Dry-run: {{ preview.dry_run_error }}</span>
            <button v-if="preview.dry_run_log" class="btn btn-sm ml-auto" @click="showLog = !showLog">log</button>
          </div>
          <pre v-if="showLog" class="max-h-48 overflow-auto rounded-lg bg-zinc-950 p-2 text-[11px] text-zinc-300">{{ preview.dry_run_log }}</pre>

          <div v-if="preview.models?.length">
            <div class="label">Model impact</div>
            <ul class="space-y-1 text-sm">
              <li v-for="m in preview.models" :key="m.id" class="flex flex-wrap items-center gap-2">
                <span
                  class="badge"
                  :class="{
                    'bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300': m.change === 'added',
                    'bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300': m.change === 'removed',
                    'bg-sky-100 text-sky-800 dark:bg-sky-950 dark:text-sky-300': m.change === 'changed' || m.change === 'aliases',
                  }"
                  >{{ m.change }}</span
                >
                <span class="font-mono break-all">{{ m.id }}</span>
                <span v-if="m.change === 'aliases'" class="muted text-xs">{{ m.old_preset || '–' }} → {{ m.new_preset || '–' }}</span>
                <span v-if="m.unloads && m.change === 'aliases'" class="text-xs font-semibold text-amber-600">currently {{ m.status }} → reloaded briefly to activate</span>
                <span v-else-if="m.unloads" class="text-xs font-semibold text-amber-600">currently {{ m.status }} → will be unloaded</span>
                <span
                  v-if="vram[m.id]"
                  class="text-xs"
                  :class="{
                    'text-emerald-600 dark:text-emerald-400': vram[m.id].status === 'ok',
                    'text-amber-600': vram[m.id].status === 'tight',
                    'font-semibold text-red-600 dark:text-red-400': vram[m.id].status === 'over',
                  }"
                  >VRAM {{ vramLabel(vram[m.id]) }}</span
                >
              </li>
            </ul>
          </div>
          <div
            v-if="unloads().length"
            class="rounded-lg border border-amber-300 bg-amber-50 px-3 py-2 text-sm text-amber-900 dark:border-amber-800 dark:bg-amber-950/40 dark:text-amber-200"
          >
            Applying unloads {{ unloads().length }} running model{{ unloads().length > 1 ? 's' : '' }}. In-flight requests to it will fail; it reloads on the next request.
          </div>
          <p class="muted text-xs">No service restart: the router hot-reloads the preset file. Unchanged models stay loaded.</p>

          <div class="flex items-center gap-2 border-t border-zinc-200 pt-4 dark:border-zinc-800">
            <input v-model="summary" class="input" placeholder="Change note (optional)" />
            <button class="btn" @click="emit('close')">Cancel</button>
            <button class="btn btn-primary whitespace-nowrap" :disabled="!ok || !preview.changed || applying" @click="doApply">
              {{ applying ? 'Applying…' : 'Apply' }}
            </button>
          </div>
        </template>

        <template v-if="result">
          <ol class="space-y-1.5 text-sm">
            <li v-for="s in result.steps" :key="s.name" class="flex gap-2">
              <span :class="s.ok ? 'text-emerald-600' : 'text-red-600'">{{ s.ok ? '✓' : '✗' }}</span>
              <span class="w-20 font-medium">{{ s.name }}</span>
              <span class="muted min-w-0 flex-1 break-all">{{ s.detail }}</span>
              <span class="muted tabular-nums">{{ s.duration_ms }} ms</span>
            </li>
          </ol>
          <div
            class="rounded-lg px-3 py-2 text-sm"
            :class="result.ok ? 'bg-emerald-50 text-emerald-800 dark:bg-emerald-950/50 dark:text-emerald-300' : 'bg-red-50 text-red-800 dark:bg-red-950/50 dark:text-red-300'"
          >
            {{ result.ok ? 'Applied.' : `Failed: ${result.error}` }}
            <span v-if="result.rolled_back"> The previous configuration was restored.</span>
          </div>
          <div class="flex justify-end">
            <button class="btn" @click="emit('close')">Close</button>
          </div>
        </template>
      </div>
    </div>
  </div>
</template>
