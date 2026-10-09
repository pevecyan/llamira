<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import type { DoctorCheck } from '../api'
import HostBackup from '../components/HostBackup.vue'
import { api } from '../state'

const checks = ref<DoctorCheck[]>([])
const error = ref('')
const loading = ref(false)
const checkedAt = ref<Date | null>(null)

async function run() {
  loading.value = true
  error.value = ''
  try {
    checks.value = (await api.value!.doctor()).checks
    checkedAt.value = new Date()
  } catch (e: any) {
    error.value = e.message
  } finally {
    loading.value = false
  }
}
onMounted(run)

const order = { fail: 0, warn: 1, ok: 2 }
const sorted = computed(() => [...checks.value].sort((a, b) => order[a.status] - order[b.status]))
const counts = computed(() => ({
  fail: checks.value.filter((c) => c.status === 'fail').length,
  warn: checks.value.filter((c) => c.status === 'warn').length,
  ok: checks.value.filter((c) => c.status === 'ok').length,
}))
const mark = { ok: '✓', warn: '!', fail: '✗' }
const cls = {
  ok: 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300',
  warn: 'bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300',
  fail: 'bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300',
}
</script>

<template>
  <div class="space-y-4">
    <div class="card">
      <div class="card-h">
        <span>Health checks</span>
        <div class="flex items-center gap-3 text-xs font-normal">
          <template v-if="checks.length">
            <span v-if="counts.fail" class="text-red-600">{{ counts.fail }} failed</span>
            <span v-if="counts.warn" class="text-amber-600">{{ counts.warn }} warning{{ counts.warn === 1 ? '' : 's' }}</span>
            <span class="muted">{{ counts.ok }} ok</span>
          </template>
          <button class="btn btn-sm" :disabled="loading" @click="run">{{ loading ? 'Checking…' : 'Check again' }}</button>
        </div>
      </div>
      <div v-if="error" class="px-4 py-3 text-sm text-red-600">{{ error }}</div>
      <div v-else-if="loading && !checks.length" class="muted px-4 py-8 text-center text-sm">Checking…</div>
      <ul v-else>
        <li v-for="c in sorted" :key="c.id" class="flex gap-3 border-b border-zinc-100 px-4 py-2.5 text-sm last:border-0 dark:border-zinc-800/60">
          <span class="badge h-fit w-6 shrink-0 justify-center !px-0" :class="cls[c.status]">{{ mark[c.status] }}</span>
          <div class="min-w-0 flex-1">
            <div class="font-medium">{{ c.title }}</div>
            <div v-if="c.detail" class="muted break-words text-xs">{{ c.detail }}</div>
            <div v-if="c.fix && c.status !== 'ok'" class="mt-1 text-xs text-zinc-700 dark:text-zinc-300">→ {{ c.fix }}</div>
          </div>
        </li>
      </ul>
      <p class="muted border-t border-zinc-200 px-4 py-2 text-xs dark:border-zinc-800">
        The same checks run with <code>llamira doctor</code> (exit code 1 when one fails).
        <template v-if="checkedAt"> Checked {{ checkedAt.toLocaleTimeString() }}.</template>
      </p>
    </div>
    <HostBackup />
  </div>
</template>
