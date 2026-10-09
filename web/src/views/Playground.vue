<script setup lang="ts">
import { computed, nextTick, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import PlaygroundCompare from '../components/PlaygroundCompare.vue'
import PlaygroundEmbed from '../components/PlaygroundEmbed.vue'
import { api, isRunning, refreshModels, state } from '../state'
import { onMarkdownClick, renderMarkdown } from '../markdown'
import { chatLimit, deleteChat, listChats, newChatId, saveChat, setChatLimit, titleFrom, type Chat } from '../chats'
import { SAMPLING_FIELDS, samplingBody, streamChat, type Sampling } from '../chatstream'

interface Msg {
  role: 'user' | 'assistant' | 'system'
  content: string
  reasoning?: string
  images?: string[] // data URLs
  imagesDropped?: number // images not kept in the saved history (too large)
  timings?: { prompt_n: number; prompt_per_second: number; predicted_n: number; predicted_per_second: number }
  error?: string
  finish?: string
  raw?: boolean // show plain text instead of rendered markdown
}

const route = useRoute()
const router = useRouter()
type Mode = 'chat' | 'compare' | 'embed'
const MODES: { key: Mode; label: string }[] = [
  { key: 'chat', label: 'Chat' },
  { key: 'compare', label: 'Compare' },
  { key: 'embed', label: 'Embeddings' },
]
const mode = ref<Mode>((['compare', 'embed'] as string[]).includes(route.query.mode as string) ? (route.query.mode as Mode) : 'chat')
watch(mode, (m) => router.replace({ query: { ...route.query, mode: m === 'chat' ? undefined : m } }))
const model = ref((route.query.model as string) ?? '')
const system = ref('')
const temperature = ref(0.7)
const maxTokens = ref(4096)
const sampling = reactive<Sampling>({})
const samplingOpen = ref(false)
const samplingSet = computed(() => Object.keys(samplingBody(sampling)).length)
function setSampling(s?: Sampling) {
  for (const f of SAMPLING_FIELDS) sampling[f.key] = s?.[f.key] ?? null
}
const messages = ref<Msg[]>([])
const input = ref('')
const images = ref<string[]>([])
const busy = ref(false)
const box = ref<HTMLElement | null>(null)
let abort: AbortController | null = null

// default to the loaded model once the model list is known (it loads asynchronously)
watch(
  () => state.models.length,
  () => {
    if (!model.value) model.value = state.models.find((m) => isRunning(m))?.id ?? state.models[0]?.id ?? ''
  },
  { immediate: true },
)

const target = computed(() => state.models.find((m) => m.id === model.value))
const vision = computed(() => target.value?.architecture?.input_modalities?.includes('image'))

// ---- chat history (browser IndexedDB, per host) ----
const chats = ref<Chat[]>([])
const chatId = ref(newChatId())
const chatCreated = ref(Date.now())
const chatTitle = ref('')
const limit = ref(chatLimit())

async function loadChats() {
  chats.value = state.host ? await listChats(state.host) : []
}
watch(() => state.host, () => { newChat(); loadChats() }, { immediate: true })

async function persist() {
  if (!messages.value.length) return
  const title = chatTitle.value || titleFrom(messages.value)
  await saveChat({
    id: chatId.value, host: state.host, title, model: model.value, system: system.value,
    temperature: temperature.value, maxTokens: maxTokens.value, sampling: { ...sampling }, messages: messages.value,
    created: chatCreated.value, updated: Date.now(),
  })
  await loadChats()
}

function newChat() {
  messages.value = []
  chatId.value = newChatId()
  chatCreated.value = Date.now()
  chatTitle.value = ''
}

function openChat(c: Chat) {
  if (busy.value) return
  chatId.value = c.id
  chatCreated.value = c.created
  chatTitle.value = c.title
  model.value = c.model
  system.value = c.system
  temperature.value = c.temperature
  maxTokens.value = c.maxTokens
  setSampling(c.sampling)
  messages.value = JSON.parse(JSON.stringify(c.messages))
  scroll()
}

async function renameChat(c: Chat) {
  const t = prompt('Chat title', c.title)?.trim()
  if (!t) return
  if (c.id === chatId.value) chatTitle.value = t
  await saveChat({ ...c, title: t })
  await loadChats()
}

async function removeChat(c: Chat) {
  if (!confirm(`Delete the chat "${c.title}"?`)) return
  await deleteChat(c.id)
  if (c.id === chatId.value) newChat()
  await loadChats()
}

function changeLimit() {
  setChatLimit(limit.value)
  limit.value = chatLimit()
}

const ago = (t: number) => {
  const m = (Date.now() - t) / 6e4
  return m < 1 ? 'just now' : m < 60 ? `${Math.round(m)} min ago` : m < 2880 ? `${Math.round(m / 60)} h ago` : `${Math.round(m / 1440)} d ago`
}

async function scroll() {
  await nextTick()
  if (box.value) box.value.scrollTop = box.value.scrollHeight
}

function addImages(ev: Event) {
  for (const f of Array.from((ev.target as HTMLInputElement).files ?? [])) {
    if (f.size > 10 << 20) continue
    const r = new FileReader()
    r.onload = () => images.value.push(r.result as string)
    r.readAsDataURL(f)
  }
  ;(ev.target as HTMLInputElement).value = ''
}

function toAPI(m: Msg) {
  if (!m.images?.length) return { role: m.role, content: m.content }
  return {
    role: m.role,
    content: [...m.images.map((url) => ({ type: 'image_url', image_url: { url } })), { type: 'text', text: m.content }],
  }
}

async function send() {
  const text = input.value.trim()
  if ((!text && !images.value.length) || busy.value || !model.value) return
  messages.value.push({ role: 'user', content: text, images: images.value.length ? [...images.value] : undefined })
  input.value = ''
  images.value = []
  const reply: Msg = { role: 'assistant', content: '' }
  messages.value.push(reply)
  const r = messages.value[messages.value.length - 1] // reactive proxy
  busy.value = true
  abort = new AbortController()
  scroll()
  const history = [...(system.value.trim() ? [{ role: 'system', content: system.value.trim() }] : []), ...messages.value.slice(0, -1).map(toAPI)]
  await streamChat(
    api.value!.base,
    { model: model.value, messages: history, temperature: temperature.value, max_tokens: maxTokens.value, ...samplingBody(sampling) },
    r,
    abort.signal,
    scroll,
  )
  busy.value = false
  abort = null
  refreshModels()
  persist()
}

function stop() {
  abort?.abort()
}

function onKey(e: KeyboardEvent) {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    send()
  }
}
</script>

<template>
  <div class="grid gap-4 lg:grid-cols-[1fr_280px]">
    <PlaygroundCompare v-if="mode === 'compare'" :system="system" :temperature="temperature" :max-tokens="maxTokens" :sampling="sampling" />
    <PlaygroundEmbed v-else-if="mode === 'embed'" :model="model" />
    <div v-else class="card flex h-[calc(100vh-9rem)] min-h-[420px] flex-col">
      <div ref="box" class="flex-1 space-y-4 overflow-auto p-4">
        <p v-if="!messages.length" class="muted py-10 text-center text-sm">
          Chat with a model to sanity-check a preset. Requests go through the router like any client;
          <template v-if="target && !isRunning(target)"> the model is loaded on the first message.</template>
        </p>
        <div v-for="(m, i) in messages" :key="i" :class="m.role === 'user' ? 'flex justify-end' : ''">
          <div
            class="max-w-[85%] rounded-2xl px-4 py-2.5 text-sm"
            :class="m.role === 'user' ? 'bg-emerald-600 text-white' : 'bg-zinc-100 dark:bg-zinc-800'"
          >
            <div v-if="m.images" class="mb-2 flex flex-wrap gap-2">
              <img v-for="(src, k) in m.images" :key="k" :src="src" class="h-24 rounded-lg object-cover" />
            </div>
            <div v-if="m.imagesDropped" class="mb-1 text-[11px] opacity-75">{{ m.imagesDropped }} image(s) not kept in history</div>
            <details v-if="m.reasoning" class="mb-2 text-xs">
              <summary class="cursor-pointer text-zinc-500">thinking ({{ m.reasoning.length }} chars)</summary>
              <div v-if="m.raw" class="mt-1 whitespace-pre-wrap text-zinc-500">{{ m.reasoning }}</div>
              <div v-else class="md mt-1 !text-xs text-zinc-500" @click="onMarkdownClick" v-html="renderMarkdown(m.reasoning)" />
            </details>
            <div v-if="m.role !== 'assistant' || m.raw" class="whitespace-pre-wrap break-words">{{ m.content }}</div>
            <div v-else class="md" @click="onMarkdownClick" v-html="renderMarkdown(m.content)" />
            <span v-if="busy && i === messages.length - 1 && m.role === 'assistant'" class="animate-pulse">▍</span>
            <div v-if="m.error" class="mt-1 text-xs text-red-600 dark:text-red-400">{{ m.error }}</div>
            <div v-if="m.finish === 'length'" class="mt-1 text-xs text-amber-700 dark:text-amber-300">
              Stopped at the max-token limit{{ m.content ? '' : ' while still thinking' }}: raise "Max tokens".
            </div>
            <div v-if="m.role === 'assistant' && (m.timings || m.content)" class="mt-1.5 flex flex-wrap items-center gap-x-3 text-[11px] text-zinc-500 tabular-nums">
              <span v-if="m.timings">
                {{ m.timings.prompt_n }} prompt tok @ {{ Math.round(m.timings.prompt_per_second) }} t/s ·
                {{ m.timings.predicted_n }} tok @ {{ m.timings.predicted_per_second.toFixed(1) }} t/s
              </span>
              <button class="ml-auto underline-offset-2 hover:underline" @click="m.raw = !m.raw">{{ m.raw ? 'formatted' : 'raw text' }}</button>
            </div>
          </div>
        </div>
      </div>
      <div class="border-t border-zinc-200 p-3 dark:border-zinc-800">
        <div v-if="images.length" class="mb-2 flex flex-wrap gap-2">
          <div v-for="(src, k) in images" :key="k" class="relative">
            <img :src="src" class="h-16 rounded-lg object-cover" />
            <button class="absolute -top-1 -right-1 rounded-full bg-zinc-900 px-1.5 text-xs text-white" @click="images.splice(k, 1)">✕</button>
          </div>
        </div>
        <div class="flex items-end gap-2">
          <label v-if="vision" class="btn h-10 cursor-pointer" title="Attach images">
            📎<input type="file" accept="image/*" multiple class="hidden" @change="addImages" />
          </label>
          <textarea v-model="input" rows="2" class="input min-h-10 flex-1 resize-none" placeholder="Message… (Enter to send, Shift+Enter for a new line)" @keydown="onKey" />
          <button v-if="busy" class="btn h-10" @click="stop">Stop</button>
          <button v-else class="btn btn-primary h-10" :disabled="!model || (!input.trim() && !images.length)" @click="send">Send</button>
        </div>
      </div>
    </div>

    <aside class="card h-fit space-y-3 p-4 text-sm">
      <div class="flex rounded-lg border border-zinc-200 p-0.5 text-xs dark:border-zinc-800" role="tablist">
        <button
          v-for="md in MODES"
          :key="md.key"
          role="tab"
          :aria-selected="mode === md.key"
          class="flex-1 rounded-md px-2 py-1 font-medium"
          :class="mode === md.key ? 'bg-zinc-100 dark:bg-zinc-800' : 'text-zinc-500 hover:text-zinc-800 dark:hover:text-zinc-200'"
          :disabled="busy"
          @click="mode = md.key"
        >
          {{ md.label }}
        </button>
      </div>
      <div v-if="mode !== 'compare'">
        <label class="label">Model</label>
        <select v-model="model" class="input font-mono text-xs">
          <option v-for="m in state.models" :key="m.id" :value="m.id">{{ m.id }}{{ isRunning(m) ? ' (loaded)' : '' }}</option>
        </select>
        <p v-if="target && !isRunning(target)" class="mt-1 text-xs text-amber-700 dark:text-amber-300">Not loaded: the first message loads it (and may unload another model).</p>
      </div>
      <template v-if="mode !== 'embed'">
      <div>
        <label class="label">System prompt</label>
        <textarea v-model="system" rows="3" class="input text-xs" placeholder="optional" />
      </div>
      <div class="grid grid-cols-2 gap-2">
        <div>
          <label class="label">Temperature</label>
          <input v-model.number="temperature" type="number" step="0.1" min="0" max="2" class="input" />
        </div>
        <div>
          <label class="label">Max tokens</label>
          <input v-model.number="maxTokens" type="number" min="1" class="input" />
        </div>
      </div>
      <div>
        <button type="button" class="label !mb-0 hover:text-zinc-800 dark:hover:text-zinc-200" @click="samplingOpen = !samplingOpen">
          {{ samplingOpen ? '▾' : '▸' }} Sampling<template v-if="samplingSet"> ({{ samplingSet }} set)</template>
        </button>
        <div v-if="samplingOpen" class="mt-2 grid grid-cols-2 gap-2">
          <div v-for="sf in SAMPLING_FIELDS" :key="sf.key" :title="sf.hint">
            <label class="label">{{ sf.label }}</label>
            <input
              :value="sampling[sf.key] ?? ''"
              type="number"
              :step="sf.step"
              :min="sf.min"
              :max="sf.max"
              class="input"
              :placeholder="sf.hint.replace(/^llama\.cpp /, '')"
              @input="sampling[sf.key] = ($event.target as HTMLInputElement).value === '' ? null : Number(($event.target as HTMLInputElement).value)"
            />
          </div>
          <p class="muted col-span-2 text-[11px]">Empty fields use the preset's or llama.cpp's default.</p>
        </div>
      </div>
      </template>
      <template v-if="mode === 'chat'">
      <button class="btn w-full" :disabled="busy" @click="newChat">New chat</button>
      <div class="border-t border-zinc-200 pt-3 dark:border-zinc-800">
        <div class="mb-1.5 flex items-center justify-between">
          <span class="label !mb-0">Recent chats</span>
          <label class="muted flex items-center gap-1 text-[11px]" title="How many chats this browser keeps (per host)">
            keep <input v-model.number="limit" type="number" min="1" max="500" class="input !w-14 !px-1 !py-0 text-[11px]" @change="changeLimit" />
          </label>
        </div>
        <ul class="max-h-[40vh] space-y-0.5 overflow-auto">
          <li
            v-for="c in chats"
            :key="c.id"
            class="group flex items-center gap-1 rounded-lg px-2 py-1.5 text-xs"
            :class="c.id === chatId ? 'bg-emerald-50 dark:bg-emerald-950/50' : 'hover:bg-zinc-100 dark:hover:bg-zinc-800'"
          >
            <button class="min-w-0 flex-1 text-left" :disabled="busy" @click="openChat(c)">
              <div class="truncate font-medium">{{ c.title }}</div>
              <div class="muted truncate text-[10px]">{{ c.model.split('/').pop() }} · {{ ago(c.updated) }}</div>
            </button>
            <button class="muted px-1 sm:hidden sm:group-hover:block" title="Rename" @click="renameChat(c)">✎</button>
            <button class="muted px-1 hover:text-red-600 sm:hidden sm:group-hover:block" title="Delete" @click="removeChat(c)">✕</button>
          </li>
          <li v-if="!chats.length" class="muted px-2 py-1 text-xs">No saved chats yet.</li>
        </ul>
        <p class="muted mt-1.5 text-[10px]">Stored in this browser only. Images over 300 KB are not kept.</p>
      </div>
      </template>
    </aside>
  </div>
</template>
