<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import type { AlertEvent, AlertSettings } from '../api'
import { api } from '../state'
import Schedules from '../components/Schedules.vue'

const st = ref<AlertSettings | null>(null)
const recent = ref<AlertEvent[]>([])
const error = ref('')
const saved = ref(false)
const testing = ref(false)
const testResult = ref<AlertEvent | null>(null)
const hasChannel = computed(() => !!st.value && !!(st.value.webhook_url || st.value.ntfy_url || (st.value.telegram_token && st.value.telegram_chat)))

async function load() {
  try {
    const r = await api.value!.alerts()
    st.value = r.settings
    recent.value = r.recent
  } catch (e: any) {
    error.value = e.message
  }
}
onMounted(load)

async function save() {
  error.value = ''
  try {
    st.value = await api.value!.saveAlerts(st.value!)
    saved.value = true
    setTimeout(() => (saved.value = false), 1500)
  } catch (e: any) {
    error.value = e.message
  }
}

async function test() {
  testing.value = true
  try {
    await save()
    testResult.value = await api.value!.testAlert()
    await load()
  } catch (e: any) {
    error.value = e.message
  } finally {
    testing.value = false
  }
}

const sevCls: Record<string, string> = {
  critical: 'bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300',
  warning: 'bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300',
  info: 'bg-sky-100 text-sky-800 dark:bg-sky-950 dark:text-sky-300',
}
</script>

<template>
  <div class="space-y-4">
    <Schedules />
    <div v-if="error" class="rounded-lg bg-red-50 px-4 py-2 text-sm text-red-700 dark:bg-red-950/50 dark:text-red-300">{{ error }}</div>
    <div v-if="st" class="card p-4">
      <div class="mb-1 font-semibold">Alerts</div>
      <p class="muted mb-4 text-sm">
        Events go to every channel you fill in: a webhook (JSON, e.g. an n8n <span class="font-mono">Webhook</span> node), an ntfy topic, and/or a Telegram chat.
        Checks run every 15 s; repeats of the same problem are suppressed for the cooldown.
      </p>
      <div class="grid gap-4 sm:grid-cols-2">
        <div class="sm:col-span-2">
          <label class="label">Webhook URL</label>
          <input v-model="st.webhook_url" class="input font-mono text-xs" placeholder="https://n8n.example/webhook/llama-alerts" />
        </div>
        <div>
          <label class="label">ntfy topic URL</label>
          <input v-model="st.ntfy_url" class="input font-mono text-xs" placeholder="https://ntfy.sh/my-llama-alerts" />
        </div>
        <div>
          <label class="label">ntfy access token <span class="font-normal">(protected topics only)</span></label>
          <input v-model="st.ntfy_token" type="password" autocomplete="off" class="input font-mono text-xs" placeholder="tk_…" />
        </div>
        <div>
          <label class="label">Telegram bot token <span class="font-normal">(from @BotFather)</span></label>
          <input v-model="st.telegram_token" type="password" autocomplete="off" class="input font-mono text-xs" placeholder="123456:ABC…" />
        </div>
        <div>
          <label class="label">Telegram chat id</label>
          <input v-model="st.telegram_chat" class="input font-mono text-xs" placeholder="e.g. 123456789 or -100…" />
        </div>
        <div class="space-y-2 text-sm">
          <label class="flex items-center gap-2"><input v-model="st.router_down" type="checkbox" /> Router down / recovered</label>
          <label class="flex items-center gap-2"><input v-model="st.model_failed" type="checkbox" /> Model failed to load</label>
          <label class="flex items-center gap-2"><input v-model="st.downloads" type="checkbox" /> Download finished / failed</label>
          <label class="flex items-center gap-2"><input v-model="st.schedule_failed" type="checkbox" /> Scheduled action failed</label>
          <label class="flex items-center gap-2"><input v-model="st.new_release" type="checkbox" /> New llama.cpp release</label>
        </div>
        <div class="grid grid-cols-2 gap-2">
          <div>
            <label class="label">GPU temp °C</label>
            <input v-model.number="st.gpu_temp_c" type="number" min="0" class="input" title="0 = off" />
          </div>
          <div>
            <label class="label">GPU memory %</label>
            <input v-model.number="st.gpu_vram_pct" type="number" min="0" max="100" class="input" title="0 = off. A loaded model normally fills most of its GPU, so set this high (e.g. 98)." />
          </div>
          <div>
            <label class="label">Host RAM %</label>
            <input v-model.number="st.ram_pct" type="number" min="0" max="100" class="input" title="0 = off. Host RAM in use (total − available)." />
          </div>
          <div>
            <label class="label">Swap MiB</label>
            <input v-model.number="st.swap_mb" type="number" min="0" class="input" title="0 = off. Alert when this much swap is in use: models paged out make requests slow." />
          </div>
          <div>
            <label class="label">Disk free GB</label>
            <input v-model.number="st.disk_free_gb" type="number" min="0" class="input" title="0 = off" />
          </div>
          <div>
            <label class="label">Cooldown min</label>
            <input v-model.number="st.cooldown_min" type="number" min="1" class="input" />
          </div>
          <p class="muted col-span-2 text-[11px]">0 turns a threshold off.</p>
        </div>
      </div>
      <div class="mt-4 flex flex-wrap items-center gap-2 border-t border-zinc-200 pt-4 dark:border-zinc-800">
        <button class="btn btn-primary" @click="save">{{ saved ? 'Saved' : 'Save' }}</button>
        <button class="btn" :disabled="testing || !hasChannel" @click="test">{{ testing ? 'Sending…' : 'Send test alert' }}</button>
        <span v-if="testResult" class="text-sm" :class="testResult.sent ? 'text-emerald-600' : 'text-red-600'">
          {{ testResult.sent ? `Delivered (${testResult.channels?.join(', ')}).` : `Not delivered: ${testResult.error}` }}
        </span>
      </div>
      <details class="mt-3 text-xs">
        <summary class="muted cursor-pointer">Webhook payload format</summary>
        <pre class="mt-2 overflow-auto rounded-lg bg-zinc-950 p-3 text-zinc-200">{
  "time": "2026-10-09T08:00:00Z", "host": "llama",
  "event": "model_failed",      // router_down, router_up, model_failed, download_finished,
                                // download_failed, gpu_temp, gpu_vram, ram_high, swap_used, disk_low,
                                // schedule_failed, llamacpp_release, test
  "severity": "critical",       // info, warning, critical
  "message": "model … failed to load (exit code 1)",
  "model": "org/repo:Q4_K_M", "data": { "exit_code": 1 }
}</pre>
      </details>
    </div>

    <div class="card">
      <div class="card-h">Recent alerts</div>
      <ul>
        <li v-for="(a, i) in recent" :key="i" class="flex flex-wrap items-center gap-2 border-b border-zinc-100 px-4 py-2 text-sm last:border-0 dark:border-zinc-800/60">
          <span class="badge" :class="sevCls[a.severity]">{{ a.event }}</span>
          <span class="min-w-0 flex-1">{{ a.message }}</span>
          <span class="muted text-xs" :title="a.error">
            {{ a.sent ? `sent${a.channels?.length ? ' (' + a.channels.join(', ') + ')' : ''}` : 'not sent' }} · {{ new Date(a.time).toLocaleString() }}
          </span>
        </li>
        <li v-if="!recent.length" class="muted px-4 py-6 text-center text-sm">No alerts yet.</li>
      </ul>
    </div>
  </div>
</template>
