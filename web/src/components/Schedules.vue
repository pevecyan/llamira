<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import type { ScheduleRule } from '../api'
import { api, refreshModels, state } from '../state'

const rules = ref<ScheduleRule[]>([])
const saved = ref<string>('[]')
const error = ref('')
const notice = ref('')
const tz = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']

async function load() {
  try {
    rules.value = await api.value!.schedules()
    saved.value = JSON.stringify(rules.value)
  } catch (e: any) {
    error.value = e.message
  }
}
onMounted(load)
const dirty = computed(() => JSON.stringify(rules.value) !== saved.value)

function add() {
  rules.value.push({ id: '', enabled: true, time: '23:00', days: [], tz, action: 'unload-all' })
}

function toggleDay(r: ScheduleRule, d: number) {
  r.days = r.days.includes(d) ? r.days.filter((x) => x !== d) : [...r.days, d].sort()
}

async function save() {
  error.value = ''
  try {
    rules.value = await api.value!.saveSchedules(rules.value)
    saved.value = JSON.stringify(rules.value)
    notice.value = 'Saved.'
    setTimeout(() => (notice.value = ''), 1500)
  } catch (e: any) {
    error.value = e.message
  }
}

async function runNow(r: ScheduleRule) {
  if (dirty.value) await save()
  try {
    const res = await api.value!.runSchedule(r.id)
    notice.value = res.result
    await load()
    setTimeout(refreshModels, 1000)
  } catch (e: any) {
    error.value = e.message
  }
}

const describe = (r: ScheduleRule) =>
  `${r.days.length ? r.days.map((d) => DAYS[d]).join(', ') : 'every day'} at ${r.time}${r.tz !== tz ? ` (${r.tz})` : ''}`
</script>

<template>
  <div class="card">
    <div class="card-h">
      Schedules
      <div class="flex gap-2 font-normal">
        <button class="btn btn-sm" @click="add">+ Add</button>
        <button class="btn btn-sm btn-primary" :disabled="!dirty" @click="save">Save</button>
      </div>
    </div>
    <div class="space-y-2 p-4 text-sm">
      <p class="muted">Load a model before working hours, or free the GPUs at night. Times are in {{ tz }}.</p>
      <div v-if="error" class="rounded-lg bg-red-50 px-3 py-2 text-red-700 dark:bg-red-950/50 dark:text-red-300">{{ error }}</div>
      <div v-if="notice" class="rounded-lg bg-emerald-50 px-3 py-2 text-emerald-800 dark:bg-emerald-950/50 dark:text-emerald-300">{{ notice }}</div>
      <div v-for="(r, i) in rules" :key="r.id || i" class="rounded-lg border border-zinc-200 p-3 dark:border-zinc-800" :class="{ 'opacity-60': !r.enabled }">
        <div class="flex flex-wrap items-center gap-2">
          <input v-model="r.enabled" type="checkbox" title="enabled" />
          <input v-model="r.time" type="time" class="input !w-28 !py-1" />
          <select v-model="r.action" class="input !w-auto !py-1">
            <option value="load">load</option>
            <option value="unload">unload</option>
            <option value="unload-all">unload all models</option>
          </select>
          <select v-if="r.action !== 'unload-all'" v-model="r.model" class="input min-w-0 flex-1 !py-1 font-mono text-xs">
            <option :value="undefined" disabled>model…</option>
            <option v-for="m in state.models" :key="m.id" :value="m.id">{{ m.id }}</option>
          </select>
          <div class="ml-auto flex gap-1.5">
            <button class="btn btn-sm" :disabled="!r.id" title="Run now" @click="runNow(r)">Run now</button>
            <button class="btn btn-sm text-red-600" @click="rules.splice(i, 1)">✕</button>
          </div>
        </div>
        <div class="mt-2 flex flex-wrap items-center gap-1">
          <button
            v-for="(d, k) in DAYS"
            :key="d"
            class="rounded-md border px-2 py-0.5 text-xs"
            :class="r.days.includes(k) ? 'border-emerald-600 bg-emerald-600 text-white' : 'border-zinc-300 dark:border-zinc-700'"
            @click="toggleDay(r, k)"
          >
            {{ d }}
          </button>
          <span class="muted ml-2 text-xs">{{ describe(r) }}</span>
        </div>
        <div v-if="r.last_run" class="muted mt-1 text-xs">last run {{ new Date(r.last_run).toLocaleString() }}: {{ r.last_result }}</div>
      </div>
      <p v-if="!rules.length" class="muted text-center text-xs">No schedules.</p>
    </div>
  </div>
</template>
