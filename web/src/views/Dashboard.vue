<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import GpuCard from '../components/GpuCard.vue'
import HostCard from '../components/HostCard.vue'
import StorageBar from '../components/StorageBar.vue'
import LogPanel from '../components/LogPanel.vue'
import StatusBadge from '../components/StatusBadge.vue'
import type { DoctorCheck, GPUPower, Model } from '../api'
import {
  api, downloadProgress, fmtBytes, fmtCtx, isRunning, localOf, presetValues, refreshModels, refreshStatus, state,
} from '../state'

const router = useRouter()
const power = ref<GPUPower[]>([])
async function loadPower() {
  try {
    power.value = await api.value!.gpuPower()
  } catch {
    /* optional */
  }
}
onMounted(loadPower)
// setup problems (llamira doctor): only failures are shown here, everything is on the Health page
const failed = ref<DoctorCheck[]>([])
onMounted(async () => {
  try {
    failed.value = (await api.value!.doctor()).checks.filter((c) => c.status === 'fail')
  } catch {
    /* optional */
  }
})
const actionError = ref('')
const restarting = ref(false)

const st = computed(() => state.status)
const loadedCount = computed(() => state.models.filter((m) => m.status.value === 'loaded').length)

const rows = computed(() =>
  [...state.models]
    .sort((a, b) => Number(isRunning(b)) - Number(isRunning(a)) || a.id.localeCompare(b.id))
    .map((m) => {
      const p = presetValues(m.status.preset)
      return {
        m,
        ctx: p['ctx-size'],
        devices: p['device'] ?? (p['tensor-split'] ? `all (${p['split-mode'] ?? 'layer'} ${p['tensor-split']})` : 'all'),
        vision: m.architecture?.input_modalities?.includes('image'),
        progress: state.progress[m.id],
        download: downloadProgress(m),
        local: localOf(m.id),
      }
    }),
)

async function act(m: Model, action: 'load' | 'unload') {
  if (!api.value) return
  actionError.value = ''
  state.busy[m.id] = true
  try {
    await (action === 'load' ? api.value.load(m.id) : api.value.unload(m.id))
    if (action === 'load') m.status.value = 'loading'
  } catch (e: any) {
    actionError.value = `${m.id}: ${e.message}`
  } finally {
    state.busy[m.id] = false
    setTimeout(refreshModels, 500)
  }
}

async function restart() {
  if (!api.value) return
  if (!confirm('Restart llama-server? Loaded models are unloaded and in-flight requests fail.')) return
  restarting.value = true
  actionError.value = ''
  try {
    await api.value.restart()
  } catch (e: any) {
    actionError.value = e.message
  } finally {
    restarting.value = false
    refreshStatus()
    refreshModels()
  }
}

async function remove(m: Model) {
  const size = localOf(m.id)?.size
  if (!confirm(`Delete ${m.id} from disk${size ? ` (${fmtBytes(size)})` : ''}? This cannot be undone.`)) return
  actionError.value = ''
  state.busy[m.id] = true
  try {
    await api.value!.deleteModel(m.id)
  } catch (e: any) {
    actionError.value = `${m.id}: ${e.message}`
  } finally {
    state.busy[m.id] = false
    refreshModels()
  }
}

async function cancelDownload(m: Model) {
  try {
    await api.value!.unload(m.id)
  } catch (e: any) {
    actionError.value = e.message
  } finally {
    setTimeout(refreshModels, 800)
  }
}

function editPreset(id: string) {
  router.push({ path: '/presets', query: { section: id } })
}

function since(s?: string) {
  if (!s) return ''
  const d = new Date(s.replace(/^\w+ /, '').replace(' UTC', 'Z').replace(' ', 'T'))
  if (Number.isNaN(d.getTime())) return s
  const mins = Math.round((Date.now() - d.getTime()) / 60000)
  if (mins < 60) return `${mins} min`
  if (mins < 48 * 60) return `${Math.round(mins / 60)} h`
  return `${Math.round(mins / 1440)} d`
}
</script>

<template>
  <div class="space-y-5">
    <RouterLink
      v-if="failed.length"
      to="/health"
      class="block rounded-lg border border-red-300 bg-red-50 px-4 py-2.5 text-sm text-red-800 hover:bg-red-100 dark:border-red-900 dark:bg-red-950/50 dark:text-red-200 dark:hover:bg-red-950"
    >
      <b>{{ failed.length === 1 ? 'A setup check failed' : `${failed.length} setup checks failed` }}:</b>
      {{ failed.map((c) => c.title).join(', ') }} · <span class="underline">see Health</span>
    </RouterLink>
    <!-- summary -->
    <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
      <!-- router + its systemd service in one card, so GPUs and the host fit in the first row -->
      <div class="card flex flex-col p-4">
        <div class="text-xs font-medium uppercase tracking-wide text-zinc-500">Router</div>
        <div class="mt-1 flex items-center gap-2 text-lg font-semibold">
          <span class="h-2.5 w-2.5 rounded-full" :class="st?.router.healthy ? 'bg-emerald-500' : 'bg-red-500'" />
          {{ st ? (st.router.healthy ? 'Healthy' : 'Down') : '…' }}
        </div>
        <div class="muted mt-1 text-xs break-words">
          <template v-if="st?.router.healthy">
            {{ st.router.props?.build_info }} · {{ loadedCount }}/{{ st.router.props?.max_instances || '∞' }} loaded
          </template>
          <template v-else>{{ st?.router.error }}</template>
        </div>
        <div class="min-h-3 flex-1" />
        <div class="flex items-end justify-between gap-2 border-t border-zinc-100 pt-3 dark:border-zinc-800">
          <div class="min-w-0">
            <div class="text-xs font-medium uppercase tracking-wide text-zinc-500">Service</div>
            <div class="mt-0.5 flex items-center gap-1.5 text-sm font-semibold">
              <span class="h-2 w-2 shrink-0 rounded-full" :class="st?.service.active_state === 'active' ? 'bg-emerald-500' : 'bg-amber-500'" />
              {{ st?.service.active_state ?? '…' }}
            </div>
            <div class="muted truncate text-xs" :title="st?.service.since">{{ st?.service.unit }} · up {{ since(st?.service.since) }}</div>
          </div>
          <button class="btn btn-sm shrink-0" :disabled="restarting" @click="restart">{{ restarting ? 'Restarting…' : 'Restart' }}</button>
        </div>
      </div>
      <GpuCard
        v-for="g in state.gpus"
        :key="g.index"
        :gpu="g"
        :history="state.gpuHistory[g.index] ?? []"
        :power="power.find((p) => p.index === g.index)"
        :device="st?.devices?.[state.gpus.indexOf(g)]"
        @changed="loadPower"
      />
      <HostCard v-if="state.hostStats" :host="state.hostStats" :history="state.cpuHistory" />
      <div v-if="st?.gpu_error" class="card p-4 text-sm text-red-600">GPU: {{ st.gpu_error }}</div>
    </div>

    <!-- models -->
    <div class="card">
      <div class="card-h">
        <span>Models <span class="muted font-normal">({{ state.models.length }})</span></span>
        <div class="flex flex-wrap items-center justify-end gap-2 font-normal">
          <span v-if="state.local?.disk" class="muted mr-1 hidden items-center gap-2 text-xs sm:flex">
            <StorageBar compact />
            {{ fmtBytes(state.local.disk.free) }} free
          </span>
          <RouterLink to="/models" class="btn btn-sm">Models</RouterLink>
          <RouterLink to="/presets" class="btn btn-sm">Edit presets</RouterLink>
        </div>
      </div>
      <div v-if="actionError" class="border-b border-zinc-200 bg-red-50 px-4 py-2 text-sm text-red-700 dark:border-zinc-800 dark:bg-red-950/40 dark:text-red-300">
        {{ actionError }}
      </div>
      <!-- phones: stacked list -->
      <ul class="md:hidden">
        <li v-for="r in rows" :key="r.m.id" class="border-b border-zinc-100 px-4 py-3 last:border-0 dark:border-zinc-800/60">
          <div class="text-sm font-medium break-all">{{ r.m.id }}</div>
          <div class="mt-1 flex flex-wrap items-center gap-1.5 text-xs">
            <StatusBadge :status="r.m.status.value" :failed="r.m.status.failed" />
            <span class="badge bg-zinc-100 text-zinc-600 dark:bg-zinc-800 dark:text-zinc-400">{{ r.m.source }}</span>
            <span v-if="r.vision" class="badge bg-violet-100 text-violet-700 dark:bg-violet-950 dark:text-violet-300">vision</span>
            <span class="muted">ctx {{ fmtCtx(r.ctx) }} · {{ r.devices }}</span>
          </div>
          <div v-if="r.download" class="mt-1.5 h-1 overflow-hidden rounded-full bg-zinc-200 dark:bg-zinc-800">
            <div class="h-full bg-emerald-500" :style="{ width: (r.download.pct * 100).toFixed(1) + '%' }" />
          </div>
          <div class="mt-2 flex flex-wrap gap-1.5">
            <button v-if="r.download" class="btn btn-sm" @click="cancelDownload(r.m)">Cancel</button>
            <template v-else>
              <button class="btn btn-sm" @click="editPreset(r.m.id)">Configure</button>
              <RouterLink class="btn btn-sm" :to="{ path: '/bench', query: { model: r.m.id } }">Test</RouterLink>
              <button v-if="r.m.source === 'cache' && !isRunning(r.m)" class="btn btn-sm text-red-600" :disabled="state.busy[r.m.id]" @click="remove(r.m)">Delete</button>
              <button v-if="isRunning(r.m)" class="btn btn-sm" :disabled="state.busy[r.m.id]" @click="act(r.m, 'unload')">Unload</button>
              <button v-else class="btn btn-sm btn-primary" :disabled="state.busy[r.m.id]" @click="act(r.m, 'load')">Load</button>
            </template>
          </div>
        </li>
        <li v-if="!state.models.length" class="muted px-4 py-8 text-center text-sm">No models reported by the router.</li>
      </ul>
      <div class="hidden overflow-x-auto md:block">
        <table class="w-full text-sm">
          <thead class="text-left text-xs text-zinc-500">
            <tr class="border-b border-zinc-200 dark:border-zinc-800">
              <th class="px-4 py-2 font-medium">Model</th>
              <th class="px-4 py-2 font-medium">Status</th>
              <th class="px-4 py-2 font-medium">Context</th>
              <th class="hidden px-4 py-2 font-medium md:table-cell">GPUs</th>
              <th class="hidden px-4 py-2 font-medium md:table-cell">Details</th>
              <th class="px-4 py-2" />
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="r in rows"
              :key="r.m.id"
              class="border-b border-zinc-100 last:border-0 hover:bg-zinc-50 dark:border-zinc-800/60 dark:hover:bg-zinc-800/30"
            >
              <td class="px-4 py-2.5">
                <div class="font-medium break-all">{{ r.m.id }}</div>
                <div class="mt-0.5 flex flex-wrap gap-1">
                  <span class="badge bg-zinc-100 text-zinc-600 dark:bg-zinc-800 dark:text-zinc-400">{{ r.m.source }}</span>
                  <span v-if="r.vision" class="badge bg-violet-100 text-violet-700 dark:bg-violet-950 dark:text-violet-300">vision</span>
                  <span v-for="a in r.m.aliases" :key="a" class="badge bg-zinc-100 text-zinc-600 dark:bg-zinc-800">{{ a }}</span>
                </div>
              </td>
              <td class="px-4 py-2.5">
                <StatusBadge :status="r.m.status.value" :failed="r.m.status.failed" />
                <div v-if="r.progress?.value !== undefined" class="mt-1 w-28">
                  <div class="h-1 overflow-hidden rounded-full bg-zinc-200 dark:bg-zinc-800">
                    <div class="h-full bg-amber-500 transition-all" :style="{ width: (r.progress.value * 100).toFixed(0) + '%' }" />
                  </div>
                  <div class="muted mt-0.5 text-[10px]">{{ r.progress.stage }}</div>
                </div>
                <div v-if="r.download" class="mt-1 w-32">
                  <div class="h-1 overflow-hidden rounded-full bg-zinc-200 dark:bg-zinc-800">
                    <div class="h-full bg-emerald-500 transition-all" :style="{ width: (r.download.pct * 100).toFixed(1) + '%' }" />
                  </div>
                  <div class="muted mt-0.5 text-[10px] tabular-nums">{{ fmtBytes(r.download.done) }} / {{ fmtBytes(r.download.total) }}</div>
                </div>
                <div v-if="r.m.status.failed" class="mt-0.5 text-xs text-red-600">exit {{ r.m.status.exit_code }}</div>
              </td>
              <td class="px-4 py-2.5 tabular-nums">
                {{ fmtCtx(r.ctx) }}
                <div v-if="r.m.meta?.n_ctx_train" class="muted text-xs">train {{ fmtCtx(r.m.meta.n_ctx_train) }}</div>
              </td>
              <td class="hidden px-4 py-2.5 text-xs md:table-cell">{{ r.devices }}</td>
              <td class="muted hidden px-4 py-2.5 text-xs md:table-cell">
                <template v-if="r.m.meta?.size">
                  {{ r.m.meta.ftype }} · {{ fmtBytes(r.m.meta.size) }} ·
                  {{ ((r.m.meta.n_params ?? 0) / 1e9).toFixed(1) }}B params
                </template>
                <template v-else-if="r.local">
                  {{ r.local.gguf?.file_type ?? r.local.tag }} · {{ fmtBytes(r.local.size) }}<template v-if="r.local.gguf"> · {{ r.local.gguf.architecture }}</template>
                </template>
                <template v-else>–</template>
              </td>
              <td class="px-4 py-2.5">
                <div class="flex flex-wrap justify-end gap-1.5">
                  <button v-if="r.download" class="btn btn-sm" @click="cancelDownload(r.m)">Cancel</button>
                  <template v-else>
                  <button class="btn btn-sm" @click="editPreset(r.m.id)">Configure</button>
                  <RouterLink class="btn btn-sm" :to="{ path: '/playground', query: { model: r.m.id } }" title="Chat">Chat</RouterLink>
                  <RouterLink class="btn btn-sm" :to="{ path: '/bench', query: { model: r.m.id } }" title="Speed test">Test</RouterLink>
                  <button
                    v-if="r.m.source === 'cache' && !isRunning(r.m)"
                    class="btn btn-sm text-red-600"
                    title="Delete from disk"
                    :disabled="state.busy[r.m.id]"
                    @click="remove(r.m)"
                  >
                    Delete
                  </button>
                  <button
                    v-if="isRunning(r.m)"
                    class="btn btn-sm"
                    :disabled="state.busy[r.m.id]"
                    @click="act(r.m, 'unload')"
                  >
                    Unload
                  </button>
                  <button
                    v-else
                    class="btn btn-sm btn-primary"
                    :disabled="state.busy[r.m.id] || r.m.status.value === 'downloading'"
                    @click="act(r.m, 'load')"
                  >
                    Load
                  </button>
                  </template>
                </div>
              </td>
            </tr>
            <tr v-if="!state.models.length">
              <td colspan="6" class="muted px-4 py-8 text-center">No models reported by the router.</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <LogPanel />
  </div>
</template>
