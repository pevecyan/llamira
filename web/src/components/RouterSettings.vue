<script setup lang="ts">
import { onMounted, ref } from 'vue'
import type { RouterSettings } from '../api'
import { api, refreshModels, refreshStatus, state } from '../state'

const rs = ref<RouterSettings | null>(null)
const form = ref({ models_max: 1, sleep: -1, autoload: true })
const busy = ref('')
const error = ref('')
const notice = ref('')
const key = ref('')

async function load() {
  try {
    rs.value = await api.value!.routerSettings()
    form.value = { models_max: rs.value.models_max, sleep: rs.value.sleep_idle_seconds, autoload: rs.value.models_autoload }
  } catch (e: any) {
    error.value = e.message
  }
}
onMounted(load)

async function set(k: string, v: string, msg: string) {
  if (!confirm(`${msg}\n\nllama-server restarts (loaded models are unloaded, in-flight requests fail). If it is not healthy afterwards the previous configuration is restored.`)) return
  busy.value = k
  error.value = ''
  notice.value = ''
  try {
    await api.value!.setRouterSetting(k, v)
    notice.value = 'Router restarted with the new setting.'
    key.value = ''
    await load()
    refreshStatus()
    refreshModels()
  } catch (e: any) {
    error.value = e.message
  } finally {
    busy.value = ''
  }
}

async function reveal() {
  key.value = (await api.value!.routerKey()).key
}

const sleepOptions = [
  { v: -1, l: 'off' },
  { v: 300, l: '5 min' },
  { v: 900, l: '15 min' },
  { v: 3600, l: '1 hour' },
  { v: 14400, l: '4 hours' },
]
const routerBase = () => `http://${state.info?.hostname ?? location.hostname}:${rs.value?.port || 8080}/v1`
</script>

<template>
  <div class="card">
    <div class="card-h">Router settings</div>
    <div v-if="!rs" class="muted px-4 py-4 text-sm">{{ error || 'Loading…' }}</div>
    <div v-else class="space-y-4 p-4 text-sm">
      <div v-if="error" class="rounded-lg bg-red-50 px-3 py-2 text-red-700 dark:bg-red-950/50 dark:text-red-300">{{ error }}</div>
      <div v-if="notice" class="rounded-lg bg-emerald-50 px-3 py-2 text-emerald-800 dark:bg-emerald-950/50 dark:text-emerald-300">{{ notice }}</div>

      <div class="grid gap-4 sm:grid-cols-3">
        <div>
          <label class="label">Models loaded at once</label>
          <div class="flex gap-2">
            <input v-model.number="form.models_max" type="number" min="0" max="64" class="input" />
            <button class="btn btn-sm" :disabled="!!busy || form.models_max === rs.models_max" @click="set('models-max', String(form.models_max), `Set models-max to ${form.models_max}?`)">
              Apply
            </button>
          </div>
          <p class="muted mt-1 text-xs">0 = unlimited. With 1, loading a model unloads the other.</p>
        </div>
        <div>
          <label class="label">Unload when idle</label>
          <div class="flex gap-2">
            <select v-model.number="form.sleep" class="input">
              <option v-for="o in sleepOptions" :key="o.v" :value="o.v">{{ o.l }}</option>
              <option v-if="!sleepOptions.some((o) => o.v === form.sleep)" :value="form.sleep">{{ form.sleep }} s</option>
            </select>
            <button class="btn btn-sm" :disabled="!!busy || form.sleep === rs.sleep_idle_seconds" @click="set('sleep-idle-seconds', String(form.sleep), 'Change idle sleep?')">Apply</button>
          </div>
          <p class="muted mt-1 text-xs">Frees VRAM after inactivity; the next request wakes the model.</p>
        </div>
        <div>
          <label class="label">Autoload on request</label>
          <div class="flex items-center gap-2">
            <label class="flex items-center gap-2"><input v-model="form.autoload" type="checkbox" /> enabled</label>
            <button class="btn btn-sm" :disabled="!!busy || form.autoload === rs.models_autoload" @click="set('models-autoload', String(form.autoload), `${form.autoload ? 'Enable' : 'Disable'} autoload?`)">Apply</button>
          </div>
          <p class="muted mt-1 text-xs">Off: requests to unloaded models fail until loaded here.</p>
        </div>
      </div>

      <div class="border-t border-zinc-200 pt-4 dark:border-zinc-800">
        <div class="flex flex-wrap items-center gap-2">
          <span class="font-medium">API key</span>
          <span class="badge" :class="rs.api_key ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300' : 'bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300'">
            {{ rs.api_key ? 'required' : 'off — the router on :' + (rs.port || 8080) + ' is open to the network' }}
          </span>
          <div class="ml-auto flex gap-1.5">
            <template v-if="rs.api_key">
              <button class="btn btn-sm" @click="reveal">Show key</button>
              <button class="btn btn-sm" :disabled="!!busy" @click="set('api-key', 'rotate', 'Generate a new API key? Every client must be updated.')">Rotate</button>
              <button class="btn btn-sm text-red-600" :disabled="!!busy" @click="set('api-key', 'off', 'Turn off the API key? The router accepts unauthenticated requests again.')">Turn off</button>
            </template>
            <button v-else class="btn btn-sm btn-primary" :disabled="!!busy"
              @click="set('api-key', 'on', 'Require an API key on the router?\n\nLiteLLM, OpenWebUI, n8n and any other client stop working until they send the key (Authorization: Bearer …).')">
              Require API key
            </button>
          </div>
        </div>
        <div v-if="key" class="mt-2 rounded-lg bg-zinc-100 p-3 text-xs dark:bg-zinc-800">
          <div class="font-mono break-all select-all">{{ key }}</div>
          <ul class="muted mt-2 list-disc space-y-0.5 pl-4">
            <li>LiteLLM: <span class="font-mono">api_key: "{{ key.slice(0, 12) }}…"</span> with <span class="font-mono">api_base: {{ routerBase() }}</span></li>
            <li>OpenWebUI: Admin → Connections → OpenAI API → key for {{ routerBase() }}</li>
            <li>n8n: OpenAI credential with this key and base URL {{ routerBase() }}</li>
          </ul>
        </div>
        <p class="muted mt-1 text-xs">The manager itself always uses the key (stored in /etc/llamira/router-api-key, readable by root and llamira).</p>
      </div>
      <details class="text-xs">
        <summary class="muted cursor-pointer">Current command line</summary>
        <pre class="mt-1 overflow-auto rounded-lg bg-zinc-950 p-2 text-zinc-200">{{ rs.exec_start }}</pre>
      </details>
    </div>
  </div>
</template>
