<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import ModelSearch from "../components/ModelSearch.vue";
import StatusBadge from "../components/StatusBadge.vue";
import StorageBar from "../components/StorageBar.vue";
import type { RecOption, Recommendation } from "../api";
import {
  api,
  downloadProgress,
  fmtBytes,
  fmtCtx,
  isRunning,
  refreshLocal,
  refreshModels,
  state,
} from "../state";

const route = useRoute();
const router = useRouter();
type Tab = "installed" | "recommended" | "search";
const tab = ref<Tab>(
  (route.query.tab as Tab) ??
    (route.query.q || route.query.repo ? "search" : "installed"),
);
watch(tab, (t) => router.replace({ query: { ...route.query, tab: t } }));
const error = ref("");
const busy = ref<Record<string, boolean>>({});

onMounted(async () => {
  await refreshLocal();
  if (
    !route.query.tab &&
    !(state.local?.models.length || state.local?.dir_files?.length)
  )
    tab.value = "recommended";
});

// ---------- installed ----------
const installed = computed(() =>
  (state.local?.models ?? []).map((lm) => {
    const rm = state.models.find((m) => m.id === lm.id);
    return { lm, rm, refs: state.local?.preset_refs?.[lm.id] ?? [] };
  }),
);

async function act(id: string, action: "load" | "unload") {
  busy.value[id] = true;
  error.value = "";
  try {
    await (action === "load" ? api.value!.load(id) : api.value!.unload(id));
  } catch (e: any) {
    error.value = `${id}: ${e.message}`;
  } finally {
    busy.value[id] = false;
    setTimeout(refreshModels, 600);
  }
}

async function remove(id: string, size: number) {
  if (
    !confirm(
      `Delete ${id} from disk (${fmtBytes(size)})? This cannot be undone.`,
    )
  )
    return;
  busy.value[id] = true;
  error.value = "";
  try {
    await api.value!.deleteModel(id);
  } catch (e: any) {
    error.value = `${id}: ${e.message}`;
  } finally {
    busy.value[id] = false;
    refreshModels();
  }
}

// opens the preset editor with a new section prefilled for this model
function createPreset(
  id: string,
  ctx?: number,
  devices?: string[] | null,
  embed = false,
) {
  const query: Record<string, string> = { new: id };
  if (embed) query.embed = "1";
  if (ctx) query.ctx = String(ctx);
  if (devices?.length === 1) query.device = devices[0];
  router.push({ path: "/presets", query });
}

// ---------- recommended ----------
const recs = ref<Recommendation[] | null>(null);
const catalog = ref<{ version: string; source: string } | null>(null);
const recLoading = ref(false);
const showAll = ref(false);
// quant shown per repo: an index into [smaller, pick, larger] (missing ones left out)
const chosen = ref<Record<string, number>>({});
const preview = ref(0); // download size under the pointer, previewed on the storage bar

async function loadRecs(refresh = false) {
  recLoading.value = true;
  error.value = "";
  try {
    const r = await api.value!.recommend(refresh);
    recs.value = r.results;
    catalog.value = r.catalog;
    chosen.value = {};
  } catch (e: any) {
    error.value = e.message;
  } finally {
    recLoading.value = false;
  }
}
watch(
  tab,
  (t) => {
    if (t === "recommended" && !recs.value && !recLoading.value) loadRecs();
  },
  { immediate: true },
);

const tiers = [
  {
    id: "one-gpu",
    title: "Fits on one GPU",
    hint: "leaves the other GPU free",
  },
  { id: "all-gpus", title: "Needs both GPUs", hint: "split by layer" },
  {
    id: "low-quality",
    title: "Only at 3-bit",
    hint: "noticeably lower quality",
    hidden: true,
  },
  {
    id: "offload",
    title: "Needs CPU offload",
    hint: "too big for the GPUs; slow",
    hidden: true,
  },
  {
    id: "unsupported",
    title: "Needs a newer llama.cpp",
    hint: "update on the llama.cpp page",
    hidden: true,
  },
  { id: "error", title: "Could not evaluate", hint: "", hidden: true },
] as const;
const byTier = (id: string) => (recs.value ?? []).filter((r) => r.tier === id);
const optsOf = (r: Recommendation) =>
  [r.smaller, r.pick, r.larger].filter((o): o is RecOption => !!o);
const optIndex = (r: Recommendation) =>
  chosen.value[r.repo] ?? Math.max(0, optsOf(r).indexOf(r.pick!));
const opt = (r: Recommendation): RecOption | undefined =>
  optsOf(r)[optIndex(r)];
const step = (r: Recommendation, d: number) =>
  (chosen.value[r.repo] = Math.min(
    optsOf(r).length - 1,
    Math.max(0, optIndex(r) + d),
  ));
const gb = (n: number) => `${(n / 1e9).toFixed(1)} GB`;
const fmtParams = (n?: number) =>
  !n
    ? ""
    : n >= 1e9
      ? `${(n / 1e9).toFixed(n >= 1e10 ? 0 : 1)}B`
      : `${Math.round(n / 1e6)}M`;

function fitsDisk(bytes: number) {
  const free = state.local?.disk?.free;
  if (free !== undefined && bytes > free * 0.95) {
    alert(
      `Not enough disk space: ${fmtBytes(bytes)} needed, ${fmtBytes(free)} free.`,
    );
    return false;
  }
  return true;
}

// withPreset: the server adds a preset section (hf-repo, context, device) once the download finishes.
async function startDownload(
  r: Recommendation,
  o: RecOption,
  withPreset: boolean,
) {
  busy.value[o.ref] = true;
  try {
    const preset = withPreset
      ? {
          ctx: r.ctx,
          device: o.devices?.length === 1 ? o.devices[0] : "",
          embeddings: r.use.includes("embedding"),
        }
      : undefined;
    await api.value!.download(o.ref, preset);
  } catch (e: any) {
    error.value = `${o.ref}: ${e.message}`;
  } finally {
    busy.value[o.ref] = false;
  }
}

async function download(r: Recommendation, o: RecOption, withPreset: boolean) {
  if (!fitsDisk(o.size)) return;
  const what = withPreset ? " and add a preset for it when done" : "";
  if (!confirm(`Download ${o.ref} (${fmtBytes(o.size)})${what}?`)) return;
  await startDownload(r, o, withPreset);
  await Promise.all([refreshModels(), refreshLocal()]);
}

// ---------- starter set: one chat model, one coder, one embedding model ----------
const starter = computed(() => {
  const ok = (recs.value ?? []).filter(
    (r) =>
      r.pick && r.supported && (r.tier === "one-gpu" || r.tier === "all-gpus"),
  );
  const best = (use: string, prefer: "large" | "small") =>
    ok
      .filter((r) => r.use.includes(use))
      .sort(
        (a, b) =>
          (a.tier === b.tier ? 0 : a.tier === "one-gpu" ? -1 : 1) ||
          ((a.params ?? 0) - (b.params ?? 0)) * (prefer === "large" ? -1 : 1),
      )[0];
  const chat = best("chat", "large");
  return [
    chat && { role: "Chat", r: chat },
    best("coder", "large") && { role: "Coding", r: best("coder", "large")! },
    best("embedding", "large") && {
      role: "Embeddings",
      r: best("embedding", "large")!,
    },
  ].filter(Boolean) as { role: string; r: Recommendation }[];
});
const showStarter = computed(
  () =>
    !!recs.value &&
    !(state.local?.models.length || state.local?.dir_files?.length) &&
    starter.value.length > 0,
);
const starterSize = computed(() =>
  starter.value.reduce((n, s) => n + (s.r.pick?.size ?? 0), 0),
);
const starterBusy = ref(false);
async function downloadStarter() {
  if (!fitsDisk(starterSize.value)) return;
  const list = starter.value.map((s) => s.r.pick!.ref).join("\n");
  if (
    !confirm(
      `Download ${fmtBytes(starterSize.value)} and add a preset for each:\n\n${list}`,
    )
  )
    return;
  starterBusy.value = true;
  try {
    for (const s of starter.value) {
      const ref = s.r.pick!.ref;
      if (!isInstalled(s.r, ref) && !downloading(ref))
        await startDownload(s.r, s.r.pick!, true);
    }
  } finally {
    starterBusy.value = false;
    await Promise.all([refreshModels(), refreshLocal()]);
  }
}
const autoPreset = (ref: string) =>
  (state.local?.auto_presets ?? []).some(
    (m) => m.toLowerCase() === ref.toLowerCase(),
  );
const downloading = (ref: string) => {
  const m = state.models.find((x) => x.id === ref);
  return m ? downloadProgress(m) : null;
};
const isInstalled = (r: Recommendation, ref: string) =>
  r.installed.some((i) => i.toLowerCase() === ref.toLowerCase());
</script>

<template>
  <div class="space-y-4">
    <!-- tabs + storage bar stay visible while scrolling (live disk use and download preview) -->
    <div
      class="sticky top-12 z-20 -mx-4 -mt-5 space-y-3 border-b border-zinc-200 bg-zinc-50/95 px-4 pb-3 pt-5 backdrop-blur lg:top-0 dark:border-zinc-800 dark:bg-zinc-950/95"
    >
      <div class="flex flex-wrap items-center gap-3">
        <div
          class="flex overflow-hidden rounded-lg border border-zinc-300 text-sm dark:border-zinc-700"
        >
          <button
            v-for="t in [
              {
                id: 'installed',
                l: `Installed (${(state.local?.models.length ?? 0) + (state.local?.dir_files?.length ?? 0)})`,
              },
              { id: 'recommended', l: 'Recommended' },
              { id: 'search', l: 'Search Hugging Face' },
            ] as const"
            :key="t.id"
            class="px-3 py-1.5"
            :class="
              tab === t.id
                ? 'bg-zinc-900 text-white dark:bg-zinc-100 dark:text-zinc-900'
                : 'hover:bg-zinc-100 dark:hover:bg-zinc-800'
            "
            @click="tab = t.id"
          >
            {{ t.l }}
          </button>
        </div>
      </div>
      <StorageBar :pending="preview" />
    </div>
    <div
      v-if="error"
      class="rounded-lg bg-red-50 px-4 py-2 text-sm text-red-700 dark:bg-red-950/50 dark:text-red-300"
    >
      {{ error }}
    </div>

    <!-- INSTALLED -->
    <template v-if="tab === 'installed'">
      <div class="card">
        <div
          v-if="!installed.length && !state.local?.dir_files?.length"
          class="px-4 py-10 text-center text-sm"
        >
          <p class="muted">No models downloaded yet.</p>
          <button class="btn btn-primary mt-3" @click="tab = 'recommended'">
            See what fits this host
          </button>
        </div>
        <ul>
          <li
            v-for="row in installed"
            :key="row.lm.id"
            class="border-b border-zinc-100 px-4 py-3 last:border-0 dark:border-zinc-800/60"
          >
            <div class="flex flex-wrap items-start gap-3">
              <div class="min-w-0 flex-1">
                <div class="font-mono text-sm font-medium break-all">
                  {{ row.lm.id }}
                </div>
                <div class="muted mt-0.5 flex flex-wrap gap-x-3 text-xs">
                  <span
                    >{{ row.lm.gguf?.file_type ?? row.lm.tag }} ·
                    {{ fmtBytes(row.lm.size) }}</span
                  >
                  <span v-if="row.lm.gguf"
                    >{{ row.lm.gguf.architecture
                    }}<template v-if="row.lm.gguf.size_label">
                      · {{ row.lm.gguf.size_label }}</template
                    ></span
                  >
                  <span v-if="row.lm.gguf?.context_length"
                    >trained ctx {{ fmtCtx(row.lm.gguf.context_length) }}</span
                  >
                  <span v-if="row.lm.extras.length"
                    >+
                    {{
                      row.lm.extras.map((x) => x.split("/").pop()).join(", ")
                    }}</span
                  >
                  <span>{{
                    new Date(row.lm.modified).toLocaleDateString()
                  }}</span>
                </div>
                <div class="mt-1 flex flex-wrap items-center gap-1.5 text-xs">
                  <StatusBadge
                    v-if="row.rm"
                    :status="row.rm.status.value"
                    :failed="row.rm.status.failed"
                  />
                  <span
                    v-else
                    class="badge bg-zinc-100 text-zinc-600 dark:bg-zinc-800"
                    >not in router</span
                  >
                  <RouterLink
                    v-for="p in row.refs"
                    :key="p"
                    :to="{ path: '/presets', query: { section: p } }"
                    class="badge bg-sky-100 text-sky-800 hover:underline dark:bg-sky-950 dark:text-sky-300"
                  >
                    preset {{ p }}
                  </RouterLink>
                </div>
              </div>
              <div class="flex flex-wrap gap-1.5">
                <button class="btn btn-sm" @click="createPreset(row.lm.id)">
                  Create preset
                </button>
                <template v-if="row.rm">
                  <button
                    v-if="isRunning(row.rm)"
                    class="btn btn-sm"
                    :disabled="busy[row.lm.id]"
                    @click="act(row.lm.id, 'unload')"
                  >
                    Unload
                  </button>
                  <button
                    v-else
                    class="btn btn-sm btn-primary"
                    :disabled="busy[row.lm.id]"
                    @click="act(row.lm.id, 'load')"
                  >
                    Load
                  </button>
                </template>
                <button
                  class="btn btn-sm text-red-600"
                  :disabled="
                    busy[row.lm.id] ||
                    (row.rm && isRunning(row.rm)) ||
                    row.refs.length > 0
                  "
                  :title="
                    row.refs.length
                      ? 'Used by a preset'
                      : row.rm && isRunning(row.rm)
                        ? 'Unload it first'
                        : 'Delete from disk'
                  "
                  @click="remove(row.lm.id, row.lm.size)"
                >
                  Delete
                </button>
              </div>
            </div>
          </li>
          <li
            v-for="f in state.local?.dir_files ?? []"
            :key="f.path"
            class="border-b border-zinc-100 px-4 py-3 last:border-0 dark:border-zinc-800/60"
          >
            <div class="font-mono text-sm break-all">{{ f.path }}</div>
            <div class="muted mt-0.5 flex flex-wrap gap-x-3 text-xs">
              <span class="badge bg-zinc-100 text-zinc-600 dark:bg-zinc-800"
                >models dir</span
              >
              <span>{{ f.gguf?.file_type }} · {{ fmtBytes(f.size) }}</span>
              <span v-if="f.gguf">{{ f.gguf.architecture }}</span>
            </div>
          </li>
        </ul>
      </div>
    </template>

    <!-- RECOMMENDED -->
    <template v-if="tab === 'recommended'">
      <div class="flex flex-wrap items-center gap-3 text-sm">
        <p class="muted">
          Curated models, each with the best quantization that fits this host's
          GPUs at its default context (estimated from the model's GGUF header,
          with
          {{ 12 }}% headroom).
        </p>
        <button
          class="btn btn-sm ml-auto"
          :disabled="recLoading"
          @click="loadRecs(true)"
        >
          {{ recLoading ? "Evaluating…" : "Refresh" }}
        </button>
      </div>
      <div
        v-if="recLoading && !recs"
        class="card muted px-4 py-10 text-center text-sm"
      >
        Reading model headers from Hugging Face… (first time ~15 s)
      </div>
      <div
        v-if="showStarter"
        class="card border-emerald-300 p-4 dark:border-emerald-800"
      >
        <div class="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h3 class="font-semibold">Starter set</h3>
            <p class="muted text-xs">
              Nothing is installed yet. One model per job, each picked to fit
              this host; a preset is added for each when its download finishes.
            </p>
          </div>
          <button
            class="btn btn-primary"
            :disabled="starterBusy"
            @click="downloadStarter"
            @mouseenter="preview = starterSize"
            @mouseleave="preview = 0"
          >
            {{
              starterBusy
                ? "Starting…"
                : `Download all (${fmtBytes(starterSize)})`
            }}
          </button>
        </div>
        <ul class="mt-3 grid gap-2 text-sm sm:grid-cols-3">
          <li
            v-for="s in starter"
            :key="s.role"
            class="rounded-lg bg-zinc-50 px-3 py-2 dark:bg-zinc-800/60"
          >
            <div class="muted text-xs">{{ s.role }}</div>
            <div class="font-medium">{{ s.r.name }}</div>
            <div class="muted font-mono text-xs">
              {{ s.r.pick!.tag }} · {{ fmtBytes(s.r.pick!.size) }}
              <span v-if="downloading(s.r.pick!.ref)" class="text-amber-600"
                >·
                {{ (downloading(s.r.pick!.ref)!.pct * 100).toFixed(0) }}%</span
              >
            </div>
          </li>
        </ul>
      </div>
      <template v-for="t in tiers" :key="t.id">
        <div
          v-if="byTier(t.id).length && (!('hidden' in t) || showAll)"
          class="space-y-2"
        >
          <div class="flex items-baseline gap-2 pt-2">
            <h3 class="font-semibold">{{ t.title }}</h3>
            <span class="muted text-xs">{{ t.hint }}</span>
          </div>
          <div class="grid gap-3 md:grid-cols-2">
            <div v-for="r in byTier(t.id)" :key="r.repo" class="card p-4">
              <div class="flex flex-wrap items-start justify-between gap-2">
                <div>
                  <div class="font-semibold">{{ r.name }}</div>
                  <a
                    :href="`https://huggingface.co/${r.repo}`"
                    target="_blank"
                    rel="noopener"
                    class="muted font-mono text-xs hover:underline"
                    >{{ r.repo }} ↗</a
                  >
                </div>
                <div class="flex flex-wrap gap-1">
                  <span
                    v-for="u in r.use"
                    :key="u"
                    class="badge bg-zinc-100 text-zinc-600 dark:bg-zinc-800 dark:text-zinc-300"
                    >{{ u }}</span
                  >
                  <span
                    v-if="r.params"
                    class="badge bg-zinc-100 text-zinc-600 dark:bg-zinc-800 dark:text-zinc-300"
                    >{{ fmtParams(r.params) }}</span
                  >
                </div>
              </div>
              <p class="muted mt-1.5 text-xs">{{ r.notes }}</p>
              <template v-if="opt(r)">
                <div class="mt-3 flex flex-wrap items-center gap-2 text-sm">
                  <button
                    v-if="optIndex(r) > 0"
                    class="btn btn-sm"
                    :title="`smaller: ${optsOf(r)[optIndex(r) - 1].tag}`"
                    @click="step(r, -1)"
                  >
                    ‹
                  </button>
                  <span class="font-mono font-semibold">{{ opt(r)!.tag }}</span>
                  <button
                    v-if="optIndex(r) < optsOf(r).length - 1"
                    class="btn btn-sm"
                    :title="`larger: ${optsOf(r)[optIndex(r) + 1].tag}`"
                    @click="step(r, 1)"
                  >
                    ›
                  </button>
                  <span class="muted text-xs"
                    >{{ fmtBytes(opt(r)!.size) }} download</span
                  >
                  <span
                    v-if="opt(r) !== r.pick"
                    class="badge bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300"
                  >
                    {{ opt(r)!.status === "ok" ? "alternative" : "won't fit" }}
                  </span>
                </div>
                <div class="muted mt-1 text-xs tabular-nums">
                  <template v-for="(d, k) in opt(r)!.devices ?? []" :key="d"
                    >{{ k ? " · " : "" }}{{ d }} ~{{
                      gb(opt(r)!.vram![k])
                    }}</template
                  >
                  at {{ fmtCtx(r.ctx) }} context
                </div>
                <p v-if="opt(r) === r.pick" class="mt-1 text-xs">
                  {{ r.reason }}
                </p>
              </template>
              <p v-else class="mt-2 text-xs text-red-600">{{ r.reason }}</p>
              <p
                v-if="r.tier === 'unsupported'"
                class="mt-1 text-xs text-amber-700 dark:text-amber-300"
              >
                {{ r.reason }}
              </p>
              <div v-if="opt(r)" class="mt-3 flex flex-wrap items-center gap-2">
                <template v-if="isInstalled(r, opt(r)!.ref)">
                  <span
                    class="badge bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300"
                    >installed</span
                  >
                  <button
                    class="btn btn-sm"
                    @click="
                      createPreset(
                        opt(r)!.ref,
                        r.ctx,
                        opt(r)!.devices,
                        r.use.includes('embedding'),
                      )
                    "
                  >
                    Create preset
                  </button>
                </template>
                <span
                  v-else-if="downloading(opt(r)!.ref)"
                  class="text-xs text-amber-600 tabular-nums"
                >
                  downloading
                  {{
                    (downloading(opt(r)!.ref)!.pct * 100).toFixed(0)
                  }}%<template v-if="autoPreset(opt(r)!.ref)">
                    · preset added when done</template
                  >
                </span>
                <template v-else>
                  <button
                    class="btn btn-sm btn-primary"
                    :disabled="busy[opt(r)!.ref] || !r.supported"
                    title="Download, then add a preset with this context and device"
                    @click="download(r, opt(r)!, true)"
                    @mouseenter="preview = opt(r)!.size"
                    @mouseleave="preview = 0"
                  >
                    {{ busy[opt(r)!.ref] ? "Starting…" : "Download + preset" }}
                  </button>
                  <button
                    class="btn btn-sm"
                    :disabled="busy[opt(r)!.ref] || !r.supported"
                    @click="download(r, opt(r)!, false)"
                    @mouseenter="preview = opt(r)!.size"
                    @mouseleave="preview = 0"
                  >
                    Download only
                  </button>
                </template>
                <span
                  v-if="r.installed.length && !isInstalled(r, opt(r)!.ref)"
                  class="muted text-xs"
                  >installed:
                  {{ r.installed.map((i) => i.split(":")[1]).join(", ") }}</span
                >
              </div>
            </div>
          </div>
        </div>
      </template>
      <label v-if="recs" class="flex items-center gap-2 pt-2 text-sm">
        <input v-model="showAll" type="checkbox" /> Show models that only fit at
        3-bit, need CPU offload, or need a newer llama.cpp
      </label>
      <p v-if="catalog" class="muted text-xs">
        Catalogue {{ catalog.version }} ({{ catalog.source }}).
      </p>
    </template>

    <!-- SEARCH -->
    <ModelSearch v-if="tab === 'search'" @preview="preview = $event" />
  </div>
</template>
