<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import type { KV, PresetDoc } from '../api'
import { api, state } from '../state'
import { copyText } from '../markdown'

const router = useRouter()
const base = ref<string>('') // server text
const doc = ref<PresetDoc | null>(null) // working text
const error = ref('')
const newAlias = ref('')
const newTarget = ref('')
const copied = ref(false)

async function load() {
  try {
    const p = await api.value!.presets()
    base.value = p.text
    doc.value = { text: p.text, preamble: p.preamble, sections: p.sections }
  } catch (e: any) {
    error.value = e.message
  }
}
onMounted(load)

const isAliasKey = (k: string) => k === 'alias' || k === 'a' || k === 'LLAMA_ARG_ALIAS'
const split = (v: string) => v.split(',').map((a) => a.trim()).filter(Boolean)

// alias → section (from the working copy, the source of truth)
const aliases = computed(() => {
  const out: { alias: string; target: string; live: boolean }[] = []
  for (const s of doc.value?.sections ?? []) {
    for (const kv of s.entries) {
      if (!isAliasKey(kv.key)) continue
      for (const a of split(kv.value)) {
        const live = state.models.some((m) => m.id === s.name && m.aliases.includes(a))
        out.push({ alias: a, target: s.name, live })
      }
    }
  }
  return out.sort((a, b) => a.alias.localeCompare(b.alias))
})
const targets = computed(() => state.models.map((m) => m.id).filter((id) => id !== '*'))
const dirty = computed(() => doc.value && doc.value.text !== base.value)

function entriesOf(section: string): KV[] {
  return doc.value?.sections.find((s) => s.name === section)?.entries.map((e) => ({ ...e })) ?? []
}

async function setAliases(section: string, list: string[]) {
  const entries = entriesOf(section).filter((e) => !isAliasKey(e.key))
  if (list.length) entries.push({ key: 'alias', value: list.join(',') })
  if (!entries.length && !doc.value?.sections.find((s) => s.name === section)) return
  const op = entries.length || !targets.value.includes(section) ? 'upsert' : 'delete'
  doc.value = await api.value!.edit({ text: doc.value!.text, op, section, entries })
}

function current(section: string) {
  return entriesOf(section)
    .filter((e) => isAliasKey(e.key))
    .flatMap((e) => split(e.value))
}

async function move(alias: string, from: string, to: string) {
  error.value = ''
  try {
    await setAliases(from, current(from).filter((a) => a !== alias))
    if (to) await setAliases(to, [...current(to).filter((a) => a !== alias), alias])
  } catch (e: any) {
    error.value = e.message
  }
}

async function add() {
  const a = newAlias.value.trim()
  if (!a || !newTarget.value) return
  if (aliases.value.some((x) => x.alias === a) || targets.value.includes(a)) {
    error.value = `"${a}" is already used`
    return
  }
  await move(a, '', newTarget.value)
  newAlias.value = ''
}

function review() {
  state.pending = { text: doc.value!.text, label: 'the alias changes' }
  router.push('/presets')
}

// LiteLLM model_list entries for every alias (clients talk to llama-server directly)
const routerURL = computed(() => {
  const u = new URL(state.info?.router_url ?? 'http://127.0.0.1:8080')
  const host = ['127.0.0.1', 'localhost'].includes(u.hostname)
    ? ['127.0.0.1', 'localhost'].includes(location.hostname) ? state.info?.hostname ?? u.hostname : location.hostname
    : u.hostname
  return `http://${host}:${u.port || 80}/v1`
})
const snippet = computed(() =>
  ['model_list:', ...aliases.value.flatMap((a) => [
    `  - model_name: ${a.alias}`,
    '    litellm_params:',
    `      model: openai/${a.alias}`,
    `      api_base: ${routerURL.value}`,
    '      api_key: "none"',
  ])].join('\n'),
)
async function copy() {
  copied.value = await copyText(snippet.value)
  setTimeout(() => (copied.value = false), 1500)
}
</script>

<template>
  <div class="space-y-4">
    <div v-if="dirty" class="flex flex-wrap items-center gap-3 rounded-xl border border-amber-300 bg-amber-50 px-4 py-2.5 text-sm dark:border-amber-800 dark:bg-amber-950/60">
      <span class="font-medium text-amber-900 dark:text-amber-200">Alias changes are not live yet</span>
      <div class="ml-auto flex gap-2">
        <button class="btn btn-sm" @click="load">Discard</button>
        <button class="btn btn-sm btn-primary" @click="review">Review &amp; apply</button>
      </div>
    </div>
    <div v-if="error" class="rounded-lg bg-red-50 px-4 py-2 text-sm text-red-700 dark:bg-red-950/50 dark:text-red-300">{{ error }}</div>

    <div class="card">
      <div class="card-h">Aliases</div>
      <p class="muted px-4 pt-3 text-sm">
        Stable names for clients (LiteLLM, OpenWebUI, n8n). Point an alias at another model and clients follow without config changes.
        The router resolves aliases natively (<span class="font-mono">alias = …</span> in the preset file).
      </p>
      <div class="overflow-x-auto">
        <table class="mt-2 w-full text-sm">
          <thead class="text-left text-xs text-zinc-500">
            <tr class="border-b border-zinc-200 dark:border-zinc-800">
              <th class="px-4 py-2 font-medium">Alias</th>
              <th class="px-4 py-2 font-medium">Points to</th>
              <th class="px-4 py-2" />
            </tr>
          </thead>
          <tbody>
            <tr v-for="a in aliases" :key="a.alias" class="border-b border-zinc-100 last:border-0 dark:border-zinc-800/60">
              <td class="px-4 py-2 font-mono font-medium">
                {{ a.alias }}
                <span v-if="!a.live" class="badge ml-1 bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300" title="Not active in the router yet">pending</span>
              </td>
              <td class="px-4 py-2">
                <select class="input !py-1 font-mono text-xs" :value="a.target" @change="move(a.alias, a.target, ($event.target as HTMLSelectElement).value)">
                  <option v-for="t in targets" :key="t" :value="t">{{ t }}</option>
                  <option v-if="!targets.includes(a.target)" :value="a.target">{{ a.target }}</option>
                </select>
              </td>
              <td class="px-4 py-2 text-right">
                <button class="btn btn-sm text-red-600" @click="move(a.alias, a.target, '')">Remove</button>
              </td>
            </tr>
            <tr v-if="!aliases.length">
              <td colspan="3" class="muted px-4 py-6 text-center">No aliases yet.</td>
            </tr>
          </tbody>
        </table>
      </div>
      <form class="flex flex-wrap gap-2 border-t border-zinc-200 px-4 py-3 dark:border-zinc-800" @submit.prevent="add">
        <input v-model="newAlias" class="input font-mono sm:!w-56" placeholder="new alias, e.g. chat-default" />
        <select v-model="newTarget" class="input min-w-0 flex-1 font-mono text-xs">
          <option value="" disabled>points to…</option>
          <option v-for="t in targets" :key="t" :value="t">{{ t }}</option>
        </select>
        <button class="btn btn-primary" :disabled="!newAlias.trim() || !newTarget">Add</button>
      </form>
    </div>

    <div v-if="aliases.length" class="card">
      <div class="card-h">
        LiteLLM config
        <button class="btn btn-sm" @click="copy">{{ copied ? 'Copied' : 'Copy' }}</button>
      </div>
      <pre class="overflow-auto rounded-b-xl bg-zinc-950 p-3 text-xs text-zinc-200">{{ snippet }}</pre>
    </div>
  </div>
</template>
