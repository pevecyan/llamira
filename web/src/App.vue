<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import Icon from './components/Icon.vue'
import Login from './components/Login.vue'
import { initHosts, selectHost, signOut, state } from './state'
import { dark, toggleTheme } from './theme'

onMounted(initHosts)
const route = useRoute()

interface Link {
  to: string
  label: string
  icon: string
  dot?: string // tooltip of a notification dot
}
const groups = computed<{ title: string; links: Link[] }[]>(() => [
  ...(state.hosts.length > 1 ? [{ title: 'Hosts', links: [{ to: '/fleet', label: 'Fleet', icon: 'fleet' }] }] : []),
  {
    title: 'Run',
    links: [
      { to: '/', label: 'Dashboard', icon: 'dashboard' },
      { to: '/playground', label: 'Playground', icon: 'playground' },
    ],
  },
  {
    title: 'Models',
    links: [
      { to: '/models', label: 'Models', icon: 'models' },
      { to: '/presets', label: 'Presets', icon: 'presets' },
      { to: '/aliases', label: 'Aliases', icon: 'aliases' },
    ],
  },
  {
    title: 'Insights',
    links: [
      { to: '/usage', label: 'Usage', icon: 'usage' },
      { to: '/bench', label: 'Benchmarks', icon: 'bench' },
      { to: '/history', label: 'History', icon: 'history' },
    ],
  },
  {
    title: 'System',
    links: [
      { to: '/health', label: 'Health', icon: 'health' },
      {
        to: '/llamacpp',
        label: 'llama.cpp',
        icon: 'engine',
        dot: state.update?.newer ? `llama.cpp ${state.update.latest_version} is available (running ${state.update.current})` : undefined,
      },
      { to: '/alerts', label: 'Automation', icon: 'automation' },
    ],
  },
])

// collapsed (icons only) on desktop, remembered per browser; a drawer on small screens
const COLLAPSE_KEY = 'llamira.sidebar'
const collapsed = ref(false)
try {
  collapsed.value = localStorage.getItem(COLLAPSE_KEY) === 'collapsed'
} catch {
  /* storage unavailable */
}
watch(collapsed, (c) => {
  try {
    localStorage.setItem(COLLAPSE_KEY, c ? 'collapsed' : 'expanded')
  } catch {
    /* ignore */
  }
})
const drawer = ref(false)
watch(
  () => route.fullPath,
  () => (drawer.value = false),
)
const isActive = (to: string) => (to === '/' ? route.path === '/' : route.path.startsWith(to))
const current = computed(() => groups.value.flatMap((g) => g.links).find((l) => isActive(l.to)))
</script>

<template>
  <Login v-if="state.loginRequired" />
  <div v-else class="min-h-screen lg:flex">
    <!-- mobile top bar -->
    <header class="sticky top-0 z-30 flex h-12 items-center gap-3 border-b border-zinc-200 bg-white/90 px-4 backdrop-blur lg:hidden dark:border-zinc-800 dark:bg-zinc-950/90">
      <button class="btn btn-sm !px-2" aria-label="Open menu" @click="drawer = true"><Icon name="menu" /></button>
      <img src="/llamira-icon-64.png" alt="" class="h-7 w-7" />
      <span class="font-semibold">{{ current?.label ?? 'Llamira' }}</span>
      <span class="ml-auto h-2 w-2 rounded-full" :class="state.eventsConnected ? 'bg-emerald-500' : 'bg-zinc-400'" />
    </header>
    <div v-if="drawer" class="fixed inset-0 z-40 bg-black/40 lg:hidden" @click="drawer = false" />

    <!-- sidebar -->
    <aside
      class="fixed inset-y-0 left-0 z-50 flex w-64 flex-col border-r border-zinc-200 bg-white transition-transform dark:border-zinc-800 dark:bg-zinc-950 lg:sticky lg:top-0 lg:h-screen lg:translate-x-0"
      :class="[drawer ? 'translate-x-0' : '-translate-x-full', collapsed ? 'lg:w-16' : 'lg:w-56']"
    >
      <div class="flex items-center gap-2 px-3 py-3" :class="collapsed ? 'lg:justify-center' : ''">
        <RouterLink to="/" class="flex items-center gap-2 font-semibold" title="Llamira: Discover. Configure. Run.">
          <img src="/llamira-icon-64.png" alt="" class="h-8 w-8 shrink-0" width="32" height="32" />
          <span :class="collapsed ? 'lg:hidden' : ''">Llamira</span>
        </RouterLink>
        <button class="ml-auto rounded-lg p-1 text-zinc-500 hover:bg-zinc-100 lg:hidden dark:hover:bg-zinc-800" aria-label="Close menu" @click="drawer = false">
          <Icon name="close" />
        </button>
      </div>

      <div v-if="state.hosts.length > 1" class="px-3 pb-2" :class="collapsed ? 'lg:hidden' : ''">
        <select class="input !py-1 text-sm" :value="state.host" @change="selectHost(($event.target as HTMLSelectElement).value)">
          <option v-for="h in state.hosts" :key="h.name" :value="h.name">{{ h.name }}{{ h.connected ? '' : ' (offline)' }}</option>
        </select>
      </div>

      <nav class="flex-1 space-y-4 overflow-y-auto px-2 py-2">
        <div v-for="g in groups" :key="g.title">
          <div class="mb-1 px-2 text-[11px] font-semibold uppercase tracking-wider text-zinc-400" :class="collapsed ? 'lg:hidden' : ''">{{ g.title }}</div>
          <div v-if="collapsed" class="mx-2 mb-1 hidden border-t border-zinc-200 lg:block dark:border-zinc-800" />
          <RouterLink
            v-for="l in g.links"
            :key="l.to"
            :to="l.to"
            :title="collapsed ? l.label : undefined"
            class="flex items-center gap-3 rounded-lg px-2.5 py-2 text-sm font-medium transition"
            :class="[
              isActive(l.to)
                ? 'bg-emerald-50 text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300'
                : 'text-zinc-600 hover:bg-zinc-100 dark:text-zinc-400 dark:hover:bg-zinc-800/70',
              collapsed ? 'lg:justify-center' : '',
            ]"
          >
            <span class="relative shrink-0">
              <Icon :name="l.icon" />
              <span v-if="l.dot" class="absolute -top-0.5 -right-0.5 h-2 w-2 rounded-full bg-sky-500 ring-2 ring-white dark:ring-zinc-950" />
            </span>
            <span :class="collapsed ? 'lg:hidden' : ''" :title="l.dot">{{ l.label }}</span>
          </RouterLink>
        </div>
      </nav>

      <div class="flex items-center gap-1 border-t border-zinc-200 px-3 py-2 dark:border-zinc-800" :class="collapsed ? 'lg:flex-col' : ''">
        <span class="flex items-center gap-1.5 px-1 text-xs text-zinc-500" :title="state.eventsConnected ? 'Live updates connected' : 'Live updates disconnected'">
          <span class="h-2 w-2 rounded-full" :class="state.eventsConnected ? 'bg-emerald-500' : 'bg-zinc-400'" />
          <span :class="collapsed ? 'lg:hidden' : ''">{{ state.eventsConnected ? 'live' : 'offline' }}<template v-if="state.hosts.length === 1"> · {{ state.info?.hostname ?? state.hosts[0].name }}</template></span>
        </span>
        <div class="flex items-center gap-1" :class="collapsed ? 'lg:flex-col' : 'ml-auto'">
          <button
            class="rounded-lg p-1.5 text-zinc-500 hover:bg-zinc-100 dark:text-zinc-300 dark:hover:bg-zinc-800"
            :title="dark ? 'Switch to day mode' : 'Switch to night mode'"
            :aria-label="dark ? 'Switch to day mode' : 'Switch to night mode'"
            @click="toggleTheme"
          >
            <Icon :name="dark ? 'sun' : 'moon'" />
          </button>
          <button
            class="hidden rounded-lg p-1.5 text-zinc-500 hover:bg-zinc-100 lg:block dark:text-zinc-300 dark:hover:bg-zinc-800"
            :title="collapsed ? 'Expand sidebar' : 'Collapse sidebar'"
            :aria-label="collapsed ? 'Expand sidebar' : 'Collapse sidebar'"
            @click="collapsed = !collapsed"
          >
            <Icon :name="collapsed ? 'expand' : 'collapse'" />
          </button>
          <button
            v-if="state.canLogout"
            class="rounded-lg p-1.5 text-zinc-500 hover:bg-zinc-100 dark:text-zinc-300 dark:hover:bg-zinc-800"
            title="Log out"
            aria-label="Log out"
            @click="signOut"
          >
            <Icon name="logout" />
          </button>
        </div>
      </div>
    </aside>

    <main class="min-w-0 flex-1">
      <div class="mx-auto max-w-7xl px-4 py-5">
        <div
          v-if="state.error"
          class="mb-4 rounded-lg border border-red-300 bg-red-50 px-4 py-2.5 text-sm text-red-800 dark:border-red-900 dark:bg-red-950/50 dark:text-red-200"
        >
          {{ state.error }}
        </div>
        <div
          v-if="state.info?.read_only"
          class="mb-4 rounded-lg border border-zinc-300 bg-zinc-100 px-4 py-2 text-sm text-zinc-700 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-300"
        >
          Read-only access: you can look around, but changes need the full token.
        </div>
        <div v-if="!state.hosts.length && !state.error" class="muted py-20 text-center">Connecting…</div>
        <RouterView v-else-if="state.host" :key="state.host" />
      </div>
    </main>
  </div>
</template>
