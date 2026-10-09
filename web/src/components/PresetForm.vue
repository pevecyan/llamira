<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import type { Arg, ArgCatalog, KV, Model, VramEstimate, VramFit } from '../api'
import { api, fmtCtx } from '../state'
import VramPanel from './VramPanel.vue'

const props = defineProps<{
  name: string
  entries: KV[]
  catalog: ArgCatalog
  isGlobal: boolean
  existingModel?: Model // the router model this section overrides (section name == model id)
  isNew: boolean // not yet in the working copy
  inherited: Record<string, string> // [*] values (canonical keys)
  models: Model[]
  devices: string[] // llama.cpp device names (CUDA0, ROCm0, …)
  text: string // working copy (for the [*] defaults used by the estimate)
}>()

const emit = defineEmits<{
  save: [entries: KV[]]
  delete: []
  rename: [newName: string]
  duplicate: [entries: KV[]]
}>()

const byName = computed(() => {
  const m = new Map<string, Arg>()
  for (const a of props.catalog.args) {
    for (const n of [...a.names, ...(a.neg_names ?? [])]) m.set(n, a)
    if (a.env) m.set(a.env, a)
  }
  return m
})
const canon = (k: string) => byName.value.get(k)?.key ?? k
const argOf = (k: string) => byName.value.get(k)
const allowed = (k: string) => {
  const a = argOf(k)
  if (a?.allowed?.length) return a.allowed
  const m = a?.value?.match(/^\{(.+)\}$/)
  return m ? m[1].split(',') : []
}

// Speculative decoding fields: flag name → canonical INI key (resolved via the catalog).
const SPEC_FLAGS = ['spec-type', 'spec-draft-hf', 'spec-draft-model', 'spec-draft-n-max', 'spec-draft-n-min', 'spec-draft-p-min',
  'spec-draft-device', 'spec-draft-ngl', 'spec-draft-type-k', 'spec-draft-type-v'] as const
const SK = computed(() => Object.fromEntries(SPEC_FLAGS.map((n) => [n, canon(n)])) as Record<(typeof SPEC_FLAGS)[number], string>)
const specTypes = computed(() => (argOf('spec-type')?.value ?? '').split(',').map((t) => t.trim()).filter((t) => t && t !== 'none'))
const specOpen = ref(false)
const draftSource = ref<'hf' | 'model'>('hf')
const needsDraftModel = computed(() => /draft-(simple|eagle3|dflash|dspark)/.test(f[SK.value['spec-type']] ?? ''))

// Keys handled by dedicated form fields.
const FORM_KEYS = [
  'hf-repo', 'model', 'ctx-size', 'n-gpu-layers', 'device', 'split-mode', 'tensor-split', 'main-gpu',
  'flash-attn', 'cache-type-k', 'cache-type-v', 'parallel', 'load-on-startup', 'alias',
]
const CTX_CHOICES = [8192, 16384, 32768, 65536, 131072, 262144]

const f = reactive<Record<string, string>>({})
const devices = ref<string[]>([])
const source = ref<'hf-repo' | 'model'>('hf-repo')
const ctxMode = ref('') // '' inherit, number string, 'custom'
const advanced = ref<KV[]>([])
const newName = ref('')

function load() {
  for (const k of Object.keys(f)) delete f[k]
  advanced.value = []
  for (const kv of props.entries) {
    const c = canon(kv.key)
    if ((FORM_KEYS.includes(c) || Object.values(SK.value).includes(c)) && f[c] === undefined) f[c] = kv.value
    else advanced.value.push({ ...kv })
  }
  devices.value = (f['device'] ?? '').split(',').map((s) => s.trim()).filter(Boolean)
  source.value = f['model'] ? 'model' : 'hf-repo'
  const c = f['ctx-size']
  ctxMode.value = !c ? '' : CTX_CHOICES.includes(Number(c)) || c === '0' ? c : 'custom'
  newName.value = props.name
  draftSource.value = f[SK.value['spec-draft-model']] ? 'model' : 'hf'
  specOpen.value = SPEC_FLAGS.some((n) => f[SK.value[n]])
}
watch(() => [props.name, props.entries], load, { immediate: true, deep: true })

const gpus = computed(() => (props.devices.length ? props.devices : ['CUDA0']))
const cacheModels = computed(() => props.models.filter((m) => m.source === 'cache'))
const needsSource = computed(() => !props.isGlobal && !props.existingModel)
const trainCtx = computed(() => {
  const repo = props.existingModel?.id ?? f['hf-repo']
  return props.models.find((m) => m.id === repo)?.meta?.n_ctx_train
})

function setDevices(d: string[]) {
  devices.value = d
  f['device'] = d.length === gpus.value.length ? '' : d.join(',')
}

const advancedIssues = computed(() =>
  advanced.value.map((kv) => {
    if (!kv.key.trim()) return ''
    const k = kv.key.trim()
    if (!argOf(k) && !(k in props.catalog.preset_only)) return 'unknown option'
    if (FORM_KEYS.includes(canon(k)) || Object.values(SK.value).includes(canon(k))) return 'use the field above'
    if (props.catalog.router_owned[canon(k)]) return 'controlled by the router (ignored)'
    return ''
  }),
)

const knownKeys = computed(() => [
  ...props.catalog.args.map((a) => a.key),
  ...Object.keys(props.catalog.preset_only),
])

function collect(): KV[] {
  const out: KV[] = []
  const put = (k: string, v?: string) => {
    if (v !== undefined && String(v).trim() !== '') out.push({ key: k, value: String(v).trim() })
  }
  if (needsSource.value) {
    if (source.value === 'hf-repo') put('hf-repo', f['hf-repo'])
    else put('model', f['model'])
  } else if (!props.isGlobal) {
    // keep an explicit source on overrides if one was present
    put('hf-repo', f['hf-repo'])
    put('model', f['model'])
  }
  put('ctx-size', ctxMode.value === 'custom' ? f['ctx-size'] : ctxMode.value)
  for (const k of ['n-gpu-layers', 'device', 'split-mode', 'tensor-split', 'main-gpu', 'flash-attn', 'cache-type-k', 'cache-type-v', 'parallel']) {
    put(k, f[k])
  }
  if (f['load-on-startup'] === 'true') put('load-on-startup', 'true')
  for (const n of SPEC_FLAGS) {
    const k = SK.value[n]
    if ((n === 'spec-draft-hf' && draftSource.value !== 'hf') || (n === 'spec-draft-model' && draftSource.value !== 'model')) continue
    if ((n === 'spec-draft-hf' || n === 'spec-draft-model') && !needsDraftModel.value) continue
    put(k, f[k])
  }
  if (!props.isGlobal) put('alias', (f['alias'] ?? '').split(',').map((a) => a.trim()).filter(Boolean).join(','))
  for (const kv of advanced.value) put(kv.key.trim(), kv.value)
  return out
}

// Live VRAM estimate of the form's current (unsaved) state.
const vram = ref<VramEstimate | null>(null)
const ram = ref<{ available: number; total: number } | undefined>(undefined)
const vramErr = ref('')
const vramLoading = ref(false)
let vramTimer: number | undefined
let vramSeq = 0
function updateVram() {
  window.clearTimeout(vramTimer)
  if (props.isGlobal) return
  vramTimer = window.setTimeout(async () => {
    const seq = ++vramSeq
    vramLoading.value = true
    try {
      const r = await api.value!.vram(props.text, props.name, collect())
      if (seq !== vramSeq) return
      vram.value = r.estimate
      ram.value = r.ram
      vramErr.value = ''
    } catch (e: any) {
      if (seq !== vramSeq) return
      vram.value = null
      vramErr.value = e.message
    } finally {
      if (seq === vramSeq) vramLoading.value = false
    }
  }, 350)
}
watch([f, devices, source, ctxMode, advanced, draftSource, () => props.text, () => props.name], updateVram, { deep: true, immediate: true })

// Largest context that is estimated to fit on the selected GPUs with the other settings.
const fit = ref<VramFit | null>(null)
const fitErr = ref('')
const fitting = ref(false)
async function fitContext() {
  fitting.value = true
  fitErr.value = ''
  fit.value = null
  try {
    const entries = collect().filter((e) => e.key !== 'ctx-size')
    fit.value = (await api.value!.vramFit(props.text, props.name, entries)).fit
  } catch (e: any) {
    fitErr.value = e.message
  } finally {
    fitting.value = false
  }
}
function useCtx(n: number, cacheType?: string) {
  if (CTX_CHOICES.includes(n)) ctxMode.value = String(n)
  else {
    ctxMode.value = 'custom'
    f['ctx-size'] = String(n)
  }
  if (cacheType) {
    f['cache-type-k'] = cacheType
    f['cache-type-v'] = cacheType
  }
  fit.value = null
}
watch(() => props.name, () => ((fit.value = null), (fitErr.value = '')))

function inh(k: string) {
  const v = props.inherited[k]
  return v !== undefined && !props.isGlobal ? `inherits ${k === 'ctx-size' ? fmtCtx(v) : v} from [*]` : 'llama.cpp default'
}
function help(k: string) {
  return argOf(k)?.description ?? props.catalog.preset_only[k] ?? ''
}
</script>

<template>
  <div class="space-y-5">
    <!-- identity -->
    <div class="flex flex-wrap items-end gap-3">
      <div class="min-w-0 flex-1">
        <label class="label">{{ isGlobal ? 'Section' : 'Model ID (name clients use, e.g. in LiteLLM)' }}</label>
        <input
          v-if="!isGlobal && !existingModel"
          v-model="newName"
          class="input font-mono"
          @change="newName.trim() && newName !== name && emit('rename', newName.trim())"
        />
        <div v-else class="font-mono text-sm font-semibold break-all">[{{ name }}]</div>
      </div>
      <button
        v-if="!isGlobal && !(needsSource && !f[source])"
        class="btn btn-sm"
        title="Copy these settings (including unsaved changes) into a new preset, e.g. to compare variants on the Benchmarks page"
        @click="emit('duplicate', collect())"
      >
        Duplicate
      </button>
      <button v-if="!isNew && !isGlobal" class="btn btn-sm text-red-600" @click="emit('delete')">
        {{ existingModel ? 'Remove overrides' : 'Delete preset' }}
      </button>
    </div>
    <div v-if="!isGlobal">
      <label class="label">Aliases <span class="font-normal">(extra names clients can use, comma-separated)</span></label>
      <input v-model="f['alias']" class="input font-mono" placeholder="e.g. chat-default, gpt-4o-mini" />
    </div>
    <p v-if="isGlobal" class="muted text-sm">Defaults for every model. A model's own section overrides these.</p>
    <p v-else-if="existingModel" class="muted text-sm">
      Overrides the settings of the cached model <span class="font-mono">{{ existingModel.id }}</span>. Unset fields inherit from [*].
    </p>

    <!-- source -->
    <fieldset v-if="needsSource" class="rounded-lg border border-zinc-200 p-3 dark:border-zinc-800">
      <legend class="px-1 text-xs font-medium text-zinc-500">Model file</legend>
      <div class="mb-2 flex gap-4 text-sm">
        <label class="flex items-center gap-1.5"><input v-model="source" type="radio" value="hf-repo" /> Cached HF model</label>
        <label class="flex items-center gap-1.5"><input v-model="source" type="radio" value="model" /> GGUF path</label>
      </div>
      <select v-if="source === 'hf-repo'" v-model="f['hf-repo']" class="input font-mono">
        <option :value="undefined" disabled>Select a cached model…</option>
        <option v-for="m in cacheModels" :key="m.id" :value="m.id">{{ m.id }}</option>
      </select>
      <input v-else v-model="f['model']" class="input font-mono" placeholder="/models/my-model/model-Q4_K_M.gguf" />
      <p class="muted mt-1.5 text-xs">Several presets can use the same file with different settings.</p>
    </fieldset>

    <!-- main settings -->
    <div class="grid gap-4 sm:grid-cols-2">
      <div>
        <label class="label">Context size</label>
        <select v-model="ctxMode" class="input">
          <option value="">{{ isGlobal ? 'not set' : inh('ctx-size') }}</option>
          <option v-for="c in CTX_CHOICES" :key="c" :value="String(c)">{{ fmtCtx(c) }} ({{ c }})</option>
          <option value="0">model default (0)</option>
          <option value="custom">custom…</option>
        </select>
        <input v-if="ctxMode === 'custom'" v-model="f['ctx-size']" class="input mt-1.5" inputmode="numeric" placeholder="tokens" />
        <p v-if="trainCtx" class="muted mt-1 text-xs">Model was trained with {{ fmtCtx(trainCtx) }} context.</p>
        <div v-if="!isGlobal" class="mt-1.5 text-xs">
          <button
            type="button"
            class="btn btn-sm"
            :disabled="fitting || (needsSource && !f[source])"
            title="Largest context that the VRAM estimate says fits (not tight) on the selected GPUs with the other settings"
            @click="fitContext"
          >
            {{ fitting ? 'Fitting…' : 'Fit max context' }}
          </button>
          <span v-if="fitErr" class="ml-2 text-red-600">{{ fitErr }}</span>
          <div v-if="fit" class="mt-1.5 space-y-1">
            <div v-if="fit.ctx">
              Fits up to <b>{{ fmtCtx(fit.ctx) }}</b> ({{ fit.ctx }})<template v-if="fit.capped"> (the trained context)</template>
              <button type="button" class="ml-1 underline" @click="useCtx(fit.ctx)">use</button>
            </div>
            <div v-else class="text-amber-700 dark:text-amber-400">Not even 1K context fits with these settings.</div>
            <div v-if="fit.alt">
              With {{ fit.alt.cache_type }} KV cache: <b>{{ fmtCtx(fit.alt.ctx) }}</b> ({{ fit.alt.ctx }})
              <button type="button" class="ml-1 underline" @click="useCtx(fit.alt.ctx, fit.alt.cache_type)">use</button>
            </div>
          </div>
        </div>
      </div>
      <div>
        <label class="label">GPU layers</label>
        <input v-model="f['n-gpu-layers']" class="input" list="ngl-opts" :placeholder="inh('n-gpu-layers')" />
        <datalist id="ngl-opts"><option value="all" /><option value="auto" /><option value="999" /><option value="0" /></datalist>
      </div>
      <div>
        <label class="label">GPUs</label>
        <div class="flex flex-wrap gap-3 pt-1 text-sm">
          <label v-for="g in gpus" :key="g" class="flex items-center gap-1.5">
            <input
              type="checkbox"
              :checked="devices.length === 0 || devices.includes(g)"
              @change="
                setDevices(
                  ($event.target as HTMLInputElement).checked
                    ? [...new Set([...(devices.length ? devices : []), g])].sort()
                    : (devices.length ? devices : gpus).filter((d) => d !== g),
                )
              "
            />
            {{ g }}
          </label>
        </div>
        <p class="muted mt-1 text-xs">{{ f['device'] ? `device = ${f['device']}` : inh('device').replace('llama.cpp default', 'all GPUs') }}</p>
      </div>
      <div class="grid grid-cols-2 gap-2">
        <div>
          <label class="label">Split mode</label>
          <select v-model="f['split-mode']" class="input">
            <option :value="undefined">{{ isGlobal ? 'not set' : inherited['split-mode'] ? `inherit (${inherited['split-mode']})` : 'default (layer)' }}</option>
            <option v-for="o in allowed('split-mode')" :key="o" :value="o">{{ o }}</option>
          </select>
        </div>
        <div>
          <label class="label">Tensor split</label>
          <input v-model="f['tensor-split']" class="input" :placeholder="inherited['tensor-split'] && !isGlobal ? inherited['tensor-split'] : 'e.g. 1,1'" />
        </div>
      </div>
      <div>
        <label class="label">Flash attention</label>
        <select v-model="f['flash-attn']" class="input">
          <option :value="undefined">{{ isGlobal ? 'not set (auto)' : inh('flash-attn') }}</option>
          <option value="on">on</option>
          <option value="off">off</option>
          <option value="auto">auto</option>
        </select>
      </div>
      <div class="grid grid-cols-2 gap-2">
        <div v-for="k in ['cache-type-k', 'cache-type-v']" :key="k">
          <label class="label">KV cache {{ k.slice(-1).toUpperCase() }}</label>
          <select v-model="f[k]" class="input">
            <option :value="undefined">{{ inherited[k] && !isGlobal ? `inherit (${inherited[k]})` : 'default (f16)' }}</option>
            <option v-for="o in allowed(k)" :key="o" :value="o">{{ o }}</option>
          </select>
        </div>
      </div>
      <div>
        <label class="label">Parallel slots</label>
        <input v-model="f['parallel']" class="input" inputmode="numeric" :placeholder="inh('parallel').replace('llama.cpp default', 'auto')" />
      </div>
      <div class="flex items-end">
        <label class="flex items-center gap-2 text-sm" :title="help('load-on-startup')">
          <input
            type="checkbox"
            :checked="f['load-on-startup'] === 'true'"
            @change="f['load-on-startup'] = ($event.target as HTMLInputElement).checked ? 'true' : ''"
          />
          Load when the router starts
        </label>
      </div>
    </div>

    <!-- speculative decoding -->
    <fieldset class="rounded-lg border border-zinc-200 p-3 dark:border-zinc-800">
      <legend class="px-1">
        <button type="button" class="text-xs font-medium text-zinc-500 hover:text-zinc-800 dark:hover:text-zinc-200" @click="specOpen = !specOpen">
          {{ specOpen ? '▾' : '▸' }} Speculative decoding<template v-if="f[SK['spec-type']]">: {{ f[SK['spec-type']] }}</template>
        </button>
      </legend>
      <div v-if="specOpen" class="grid gap-3 sm:grid-cols-2">
        <div class="sm:col-span-2">
          <label class="label">Type</label>
          <select v-model="f[SK['spec-type']]" class="input">
            <option :value="undefined">{{ isGlobal ? 'off' : inherited[SK['spec-type']] ? `inherit (${inherited[SK['spec-type']]})` : 'off' }}</option>
            <option v-for="t in specTypes" :key="t" :value="t">{{ t }}</option>
          </select>
          <p class="muted mt-1 text-xs">
            <b>draft-mtp</b> uses a model's built-in multi-token-prediction head (MTP GGUFs); <b>draft-simple</b> needs a small draft model of the same family;
            <b>ngram-*</b> needs no extra model and helps with repetitive output (code, edits). Compare with Benchmarks.
          </p>
        </div>
        <template v-if="needsDraftModel">
          <div class="sm:col-span-2">
            <label class="label">Draft model</label>
            <div class="mb-1.5 flex gap-4 text-sm">
              <label class="flex items-center gap-1.5"><input v-model="draftSource" type="radio" value="hf" /> Cached HF model</label>
              <label class="flex items-center gap-1.5"><input v-model="draftSource" type="radio" value="model" /> GGUF path</label>
            </div>
            <select v-if="draftSource === 'hf'" v-model="f[SK['spec-draft-hf']]" class="input font-mono text-xs">
              <option :value="undefined" disabled>Select a cached model…</option>
              <option v-for="m in cacheModels" :key="m.id" :value="m.id">{{ m.id }}</option>
            </select>
            <input v-else v-model="f[SK['spec-draft-model']]" class="input font-mono" placeholder="/models/draft/model-Q8_0.gguf" />
          </div>
          <div>
            <label class="label">Draft GPUs</label>
            <input v-model="f[SK['spec-draft-device']]" class="input" placeholder="follows GPUs (e.g. CUDA0)" />
          </div>
          <div>
            <label class="label">Draft GPU layers</label>
            <input v-model="f[SK['spec-draft-ngl']]" class="input" placeholder="auto" />
          </div>
        </template>
        <div v-if="f[SK['spec-type']]" class="grid grid-cols-3 gap-2 sm:col-span-2">
          <div>
            <label class="label">Draft max</label>
            <input v-model="f[SK['spec-draft-n-max']]" class="input" inputmode="numeric" placeholder="3" />
          </div>
          <div>
            <label class="label">Draft min</label>
            <input v-model="f[SK['spec-draft-n-min']]" class="input" inputmode="numeric" placeholder="0" />
          </div>
          <div>
            <label class="label">Min probability</label>
            <input v-model="f[SK['spec-draft-p-min']]" class="input" inputmode="decimal" placeholder="0.00" />
          </div>
        </div>
        <div v-if="needsDraftModel" class="grid grid-cols-2 gap-2 sm:col-span-2">
          <div v-for="k in ['spec-draft-type-k', 'spec-draft-type-v'] as const" :key="k">
            <label class="label">Draft KV cache {{ k.slice(-1).toUpperCase() }}</label>
            <select v-model="f[SK[k]]" class="input">
              <option :value="undefined">default (f16)</option>
              <option v-for="o in allowed(k)" :key="o" :value="o">{{ o }}</option>
            </select>
          </div>
        </div>
      </div>
    </fieldset>

    <VramPanel v-if="!isGlobal" :estimate="vram" :ram="ram" :error="vramErr" :loading="vramLoading && !vram" />
    <p v-else class="muted text-xs">Open a preset or cached model to see a VRAM estimate with these defaults.</p>

    <!-- advanced -->
    <div>
      <div class="mb-2 flex items-center justify-between">
        <span class="label !mb-0">Advanced options</span>
        <button class="btn btn-sm" @click="advanced.push({ key: '', value: '' })">+ Add option</button>
      </div>
      <datalist id="arg-keys"><option v-for="k in knownKeys" :key="k" :value="k" /></datalist>
      <div v-for="(kv, i) in advanced" :key="i" class="mb-2">
        <div class="flex gap-2">
          <input v-model="kv.key" class="input font-mono !w-2/5" list="arg-keys" placeholder="option" />
          <input v-model="kv.value" class="input font-mono" :placeholder="argOf(kv.key)?.value ?? 'true'" />
          <button class="btn btn-sm" title="Remove" @click="advanced.splice(i, 1)">✕</button>
        </div>
        <p v-if="advancedIssues[i]" class="mt-0.5 text-xs text-red-600">{{ advancedIssues[i] }}</p>
        <p v-else-if="kv.key && help(kv.key)" class="muted mt-0.5 text-xs">{{ help(kv.key) }}</p>
      </div>
      <p v-if="!advanced.length" class="muted text-xs">Any llama-server option without dashes, e.g. <code>temp</code>, <code>jinja</code>, <code>mmproj-offload</code>.</p>
    </div>

    <div class="flex justify-end gap-2 border-t border-zinc-200 pt-4 dark:border-zinc-800">
      <button class="btn" @click="load">Reset form</button>
      <button class="btn btn-primary" :disabled="needsSource && !f[source]" @click="emit('save', collect())">
        Save to working copy
      </button>
    </div>
  </div>
</template>
