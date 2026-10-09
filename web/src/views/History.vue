<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import DiffView from '../components/DiffView.vue'
import type { ApplyRecord } from '../api'
import { api, state } from '../state'

const router = useRouter()

function restore(a: ApplyRecord, which: 'before' | 'after') {
  const text = which === 'before' ? a.before : a.after
  if (text === undefined) return
  state.pending = { text, label: `the version ${which} change #${a.id}` }
  router.push('/presets')
}

const list = ref<ApplyRecord[]>([])
const open = ref<Record<number, ApplyRecord>>({})
const error = ref('')
const loading = ref(true)

onMounted(async () => {
  try {
    list.value = await api.value!.applies()
  } catch (e: any) {
    error.value = e.message
  } finally {
    loading.value = false
  }
})

async function toggle(id: number) {
  if (open.value[id]) {
    delete open.value[id]
    return
  }
  open.value[id] = await api.value!.applyDetail(id)
}

const fmt = (t: string) => new Date(t).toLocaleString()
</script>

<template>
  <div class="card">
    <div class="card-h">Apply history</div>
    <div v-if="error" class="px-4 py-3 text-sm text-red-600">{{ error }}</div>
    <div v-else-if="loading" class="muted px-4 py-8 text-center text-sm">Loading…</div>
    <div v-else-if="!list.length" class="muted px-4 py-8 text-center text-sm">No changes have been applied yet.</div>
    <ul v-else>
      <li v-for="a in list" :key="a.id" class="border-b border-zinc-100 last:border-0 dark:border-zinc-800/60">
        <button class="flex w-full items-center gap-3 px-4 py-2.5 text-left text-sm hover:bg-zinc-50 dark:hover:bg-zinc-800/40" @click="toggle(a.id)">
          <span :class="a.ok ? 'text-emerald-600' : 'text-red-600'">{{ a.ok ? '✓' : '✗' }}</span>
          <span class="muted w-44 shrink-0 tabular-nums">{{ fmt(a.time) }}</span>
          <span class="min-w-0 flex-1 truncate font-medium">{{ a.summary }}</span>
          <span v-if="a.error" class="max-w-md truncate text-xs text-red-600">{{ a.error }}</span>
          <span class="muted text-xs">#{{ a.id }}</span>
        </button>
        <div v-if="open[a.id]" class="space-y-3 px-4 pb-4">
          <ol class="space-y-1 text-xs">
            <li v-for="s in open[a.id].steps" :key="s.name" class="flex gap-2">
              <span :class="s.ok ? 'text-emerald-600' : 'text-red-600'">{{ s.ok ? '✓' : '✗' }}</span>
              <span class="w-16 font-medium">{{ s.name }}</span>
              <span class="muted min-w-0 flex-1 break-all">{{ s.detail }}</span>
              <span class="muted tabular-nums">{{ s.duration_ms }} ms</span>
            </li>
          </ol>
          <DiffView v-if="open[a.id].diff" :diff="open[a.id].diff!" />
          <div class="flex flex-wrap gap-2">
            <button class="btn btn-sm" @click="restore(open[a.id], 'before')">Load version before this change</button>
            <button class="btn btn-sm" @click="restore(open[a.id], 'after')">Load version after this change</button>
            <span class="muted self-center text-xs">Opens it in the preset editor; nothing changes until you apply.</span>
          </div>
        </div>
      </li>
    </ul>
  </div>
</template>
