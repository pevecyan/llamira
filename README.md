<p align="center">
  <img src="docs/assets/brand/llamira-banner.png" alt="Llamira" width="420" />
</p>

# Llamira

**Your local AI model control center.** *Discover. Configure. Run.*

A lightweight, self-hosted dashboard for managing [llama.cpp](https://github.com/ggml-org/llama.cpp) models, configurations, downloads and GPU resources. It is a single Go binary with an embedded web UI. It runs next to `llama-server` in router mode and is managed from anywhere over SSH.

llama.cpp keeps responsibility for inference, routing and model lifecycle. Llamira edits the router's preset file safely, and drives the router's own API to list, load, unload, download and delete models. It is **not** an inference proxy: LiteLLM, OpenWebUI and other clients keep talking to `llama-server` directly.

## Features

- **Dashboard:** router and service status, loaded models, live GPU (NVIDIA and AMD) and host RAM/CPU telemetry.
- **Presets with a safety net:** a form and a raw editor for the router's preset file; every change is diffed, validated on a throwaway router, applied by hot reload, verified and rolled back on failure. Backups and a full history.
- **VRAM estimate:** a per-GPU fit check for unsaved settings, and *Fit max context*.
- **Models:** search Hugging Face, curated recommendations that fit your GPUs, downloads with live progress, delete, usage-based cleanup.
- **Benchmarks:** speed tests stored with each config, and power sweeps to find the efficient GPU power limit.
- **llama.cpp updater:** download or build any upstream release, validate it against your presets, switch with automatic rollback.
- **Playground:** chat, side-by-side compare and embeddings against any model.
- **Automation:** alerts (webhook, ntfy, Telegram), schedules, aliases for stable client names.
- **Operations:** health checks with fixes, usage statistics, Prometheus metrics, export/import, several hosts in one UI.
- **Access:** SSH tunnel by default; optionally on the LAN with a token and/or password, TLS and a read-only token.

## Requirements

- **The llama host:** Linux with systemd and a GPU driver (NVIDIA or AMD), reachable over SSH as root or a user with sudo (for the install). An existing llama.cpp router setup can be adopted; otherwise `install` sets llama.cpp up.
- **Your computer:** Linux, macOS or Windows with SSH access to the host.
- **To build:** Go (version in `go.mod`) and Node 22 (see `.nvmrc`).

## Quick start

```sh
make build                                   # → bin/llamira (UI embedded)

# from your computer: install on (or upgrade) a llama host over SSH
bin/llamira hosts add llama root@llama-host
bin/llamira deploy llama --adopt             # uploads itself, runs `install --adopt` remotely

# open the UI (SSH tunnel to the host's API socket)
bin/llamira connect llama
```

`--adopt` moves per-model flags from an existing `llama-server` command line into the preset file. On a host without llama.cpp, the same command installs it (see [Installing on a new host](#installing-on-a-new-host)).

To serve the UI directly on the LAN at `http(s)://<host>:7070/`:

```sh
llamira deploy llama --listen 0.0.0.0:7070                         # token login (link printed once)
llamira deploy llama --listen 0.0.0.0:7070 --tls self-signed       # same over HTTPS
llamira deploy llama --listen 0.0.0.0:7070 --read-token            # plus a GET-only token for dashboards
llamira deploy llama --listen 0.0.0.0:7070 --auth none             # no authentication (trusted LAN only)
ssh -t root@llama-host llamira password                            # optional: password login next to the token
```

## CLI

```sh
llamira connect                 # all hosts in hosts.yaml (several hosts → host switcher)

# over the same tunnel (--host NAME, or the default host)
llamira status
llamira models
llamira load  unsloth/Qwen3.5-9B-MTP-GGUF:Q5_K_M
llamira unload unsloth/Qwen3.5-9B-MTP-GGUF:Q5_K_M
llamira presets > models.ini    # edit, then:
llamira apply models.ini        # diff, validation, dry-run, model impact, confirm, apply
llamira logs -f
llamira restart
llamira doctor                  # setup checks with fixes; exit code 1 when one fails

llamira download unsloth/Qwen3.5-9B-GGUF:Q4_K_M --preset --ctx 32768 --wait
llamira delete old/Model-GGUF:Q4_K_M
llamira bench unsloth/Qwen3.5-9B-GGUF:Q4_K_M --prompt 2048 --gen 256
llamira usage --days 30
llamira power 0 150             # GPU 0 to 150 W (kept after reboot); `power 0 default` resets
llamira aliases
llamira export -o llama.json    # presets, schedules, alerts, power limits, config (secrets redacted)
llamira import llama.json       # on another host; presets go through the apply review
```

`llamira help` lists every command and flag.

Read commands (`status`, `models`, `usage`, `power`, `aliases`, `bench`, `doctor`) take `--json` and print the API response unchanged, for scripts and cron jobs.

SSH auth uses ssh-agent, `~/.ssh/config` (HostName, User, Port, IdentityFile) and default keys. It falls back to a password prompt, or `$SSHPASS` for scripts. Host keys are checked against `~/.ssh/known_hosts`. An unknown host is accepted only after you confirm it on a terminal. A changed host key is always refused.

## How llama.cpp must be run

Llamira expects `llama-server` to run as a **router with a preset file**, with **no per-model flags on the command line**.

In llama.cpp, command-line flags take precedence over presets. A flag like `--ctx-size` on the command line would silently override every per-model context size you set in the UI. Defaults therefore go in the `[*]` section of the preset file.

### 1. Preset file — `/var/lib/llamira/models.ini`

```ini
version = 1

; Global defaults for every model (managed by llamira).
[*]
ctx-size = 65536
n-gpu-layers = 999
split-mode = layer
tensor-split = 1,1

; Example: an extra id that reuses a cached GGUF with different settings
; [qwen9b-16k-gpu1]
; hf-repo = unsloth/Qwen3.5-9B-MTP-GGUF:Q5_K_M
; ctx-size = 16384
; device = CUDA1
```

Rules from llama.cpp:
- **Keys:** long CLI argument names without dashes (`n-gpu-layers`). Short names (`ngl`) and `LLAMA_ARG_*` env names also work. An unknown key is a parse error.
- **Overriding an existing model:** a section named exactly like an existing model id (e.g. `org/repo:QUANT` from the cache) overrides that model's settings.
- **New model ids:** any other section name creates a new id and needs `hf-repo = org/repo:QUANT` or `model = /abs/path.gguf`.
- **Preset-only keys:** `load-on-startup`, `stop-timeout`, `dedup-cache-models`.

### 2. systemd drop-in — `/etc/systemd/system/llama-server.service.d/llamira.conf`

```ini
# Managed by llamira. Delete this file + "systemctl daemon-reload" to revert.
[Service]
ExecStart=
ExecStart=/opt/llama.cpp/build/bin/llama-server --host 0.0.0.0 --port 8080 --models-dir /models --models-max 1 --models-preset /var/lib/llamira/models.ini
```

Only **router-level** flags belong on the command line: `--host`, `--port`, `--models-dir`, `--models-max`, `--models-preset`, and later e.g. `--api-key`. The base unit stays as it is and must keep:

```ini
Environment=LLAMA_CACHE=/models/cache           # where router downloads (HF cache layout) go
Environment=LD_LIBRARY_PATH=/usr/local/cuda/lib64
WorkingDirectory=/opt/llama.cpp
```

Apply and verify:

```sh
systemctl daemon-reload && systemctl restart llama-server
curl -s localhost:8080/models | jq '.data[] | {id, status: .status.value}'
```

### 3. How changes are applied

- **No restart is needed for preset changes.** llamira writes the file and calls `GET /models?reload=1`. The router unloads a running model only if *that* model's config changed. If the new file is invalid, the router returns HTTP 500 and keeps its previous config.
- **A restart is needed only for router-level flags** (the drop-in). A restart with an invalid preset file makes the service fail, so llamira first validates every change on a throwaway router instance (`--port <free> --no-models-autoload`, no VRAM used).

### Reverting to the plain setup

```sh
rm /etc/systemd/system/llama-server.service.d/llamira.conf
systemctl daemon-reload && systemctl restart llama-server
```

## Installing on a new host

On a host with only the GPU driver, `llamira install` (or `llamira deploy user@host` from a laptop) also installs llama.cpp:

- **Detect** the OS, architecture, GPUs (compute capability), the driver's CUDA version, `nvcc`, RAM and disk (`llamira detect` shows this).
- **Get llama.cpp.**
  - **Prebuilt** (default): the upstream Ubuntu release for this host (`cuda-13.4`, `cuda-12.8`, `vulkan`, `rocm`, or `cpu`), plus the CUDA runtime bundle. Takes a few minutes.
  - **Source** (`--llama-cpp source`): a cmake build with `GGML_CUDA` and `CMAKE_CUDA_ARCHITECTURES` from the detected GPUs. Needs the CUDA toolkit.
- **Write** `llama-server.service` with the `llamira.conf` drop-in, `/models`, and a preset file with defaults for the hardware. Then start the router and verify it.
- **Continue** with the normal install (user, sudoers rule, `llamira.service`).

Everything created is recorded in `/var/lib/llamira/install-manifest.json`. `llamira uninstall [--purge-models] [--remove-packages]` removes it. On adopted hosts, uninstall restores the original `llama-server` command line instead. Running install again continues where a previous attempt stopped.

The **llama.cpp** page offers **Download** (prebuilt, minutes) next to **Build** (from source, about 20 minutes) for every upstream release. Both are validated against the current preset file before you can switch.

### What gets installed on the host

| | |
|---|---|
| `/usr/local/bin/llamira` | the binary |
| `/etc/llamira/config.yaml` | server config (router URL, preset path, llama-server binary, …) |
| `/etc/systemd/system/llamira.service` | runs `serve` as the system user `llamira` (group `systemd-journal` for logs) |
| `/run/llamira/api.sock` | API socket (0660, used by `connect`/CLI over SSH) + the `listen` address (default `127.0.0.1:7070`) |
| `/var/lib/llamira/` | `models.ini` (router preset), `backups/`, `llamira.db` (apply history), `tls/` (self-signed certificate, with `tls: self-signed`) |
| `/etc/sudoers.d/llamira` | `llamira` may run only `systemctl restart llama-server.service` and `llamira privileged …` (builds, switch, power limits, all argument-validated) |
| `/opt/llama.cpp-builds/<tag>` | llama.cpp versions built by the updater |
| `/etc/llamira/power-limits.json`, `llamira-power.service` | persisted GPU power limits (only while one is set) |
| `llama-server.service.d/llamira.conf` | the router drop-in described above (`--adopt`) |

Downloads and deletes of models run inside the router through its API, so `llamira` never needs write access to `/models`.

## LAN access: password, TLS and a read-only token

With `listen` on a non-loopback address the UI needs `auth_token` or a password (or `allow_unauthenticated: true`). More options in `/etc/llamira/config.yaml`:
- **Password:** `sudo llamira password` sets `password_hash` (bcrypt), then `sudo systemctl restart llamira`. The UI shows a login form, and a login lasts 30 days in that browser (sign out at the bottom of the sidebar). The token keeps working next to it, for scripts and login links. After 5 wrong passwords from one address, each further try waits a minute. Setting a new password ends all logins; `llamira password --off` removes it.
- **TLS:** `tls: self-signed` generates a certificate on first start (in `data_dir/tls`, for the host name and its addresses; the browser warns once, and the SHA-256 fingerprint is in `journalctl -u llamira`). Or set `tls_cert` and `tls_key` to your own files. `install`/`deploy` take `--tls self-signed|off`.
- **Read-only token:** `read_token` allows GET requests only, for dashboards and wall displays. It cannot change anything, and cannot read the router API key, the export, or the alert settings (they hold secrets). `install`/`deploy --read-token` creates one and prints its login link.

## Features in detail

### Health checks

The **Health** page (and `llamira doctor`) checks for the mistakes that break a setup quietly, and says how to fix each one:
- per-model flags on the router command line, which override every preset
- `--models-preset` missing, or pointing at a different file than `preset_file`
- the router listening on the network without an API key
- `HF_TOKEN` set for llamira but not for the router (gated downloads fail), and `LLAMA_CACHE` differing from `router.cache_dir`
- an invalid preset file, models that failed to load, low disk, no visible GPU, an unreadable journal
- the UI on the LAN without a token, or over plain HTTP

The Dashboard shows a banner when a check fails.

### Aliases (stable names for LiteLLM / OpenWebUI)

`alias = chat-default,gpt-4o-mini` in a model's section gives it extra names that the router routes to. The **Aliases** page shows which model each alias points to. Re-pointing an alias is one apply. The page also generates the LiteLLM `model_list` snippet.

The router only re-reads aliases of models that are not running. When an alias of a loaded model changes, the apply's **aliases** step unloads that model, reloads the list and loads the model again (a short pause for requests to it).

### Benchmarks

A speed test sends a fixed prompt (512 to 32K tokens) through the router's `/completion`, with the prompt cache off and EOS ignored. It records llama.cpp's own prompt and generation speeds, load time, time to first token, and per-GPU peak VRAM, max temperature and average power during generation. Results are stored with each model's effective config so variants can be compared. Testing a model that isn't loaded unloads the current one (`models-max 1`), and it is reloaded afterwards if you choose.

**Duplicate** on a preset copies its settings (including unsaved changes) into a new `<name>-v2` preset, the quick way to a variant to compare. An override of a cached model becomes a custom preset with `hf-repo = <model id>`; aliases stay with the original.

### llama.cpp updater

The **llama.cpp** page lists upstream releases and the builds on the host.

- **Build:** `git worktree` + `cmake` into `/opt/llama.cpp-builds/<tag>`, with the CUDA flags read from the existing `build/CMakeCache.txt`. It builds only `llama-server`, as a low-priority transient systemd job (`llamira-build-<tag>`; nice 19, CPU weight 10) with a live log.
- **Validate:** the new binary must run, and a throwaway router on it must accept the current preset file.
- **Switch:** rewrites the drop-in's `ExecStart` binary, restarts, checks health and the model list, and rolls back automatically on failure.
- **Update notice:** the sidebar shows a dot on *llama.cpp* when a newer upstream release exists (GitHub is asked at most every 6 hours).
- **Speed check:** after a switch the page offers to re-run the latest benchmark with the same sizes, and shows the old and new speeds side by side.

The original `/opt/llama.cpp/build` always stays available as a fallback. Root actions go through `llamira privileged …` (sudo), which validates every argument and always uses `/etc/llamira/config.yaml`.

### Usage statistics

Llamira follows the `llama-server` journal: the router logs which model serves each request, and the model instance logs its timings. From that it keeps per-request rows for 90 days. The first start backfills 30 days of history. The **Usage** page shows requests and tokens per day (or per hour) per model, plus average speeds and when each model was last used.

- **Cleanup** lists downloaded models that nobody used for N days (requests to a preset count for the model it uses), with their size and a Delete button. A model is suggested only when the stored history is long enough to say so, and delete keeps the usual checks (not loaded, not used by a preset).
- **Export CSV** for the per-model totals and for the time series.

### Alerts

Every 15 s the manager checks for: the router down or recovered, a model failing to load, a download finishing or failing, GPU temperature or memory over a threshold, host RAM or swap use over a threshold, low disk space, a scheduled action failing, and (optional) a new llama.cpp release. Each event goes to every configured channel, with a cooldown per event:
- a **webhook** (JSON POST, e.g. n8n)
- an **ntfy** topic URL (optionally with an access token)
- a **Telegram** chat, through a bot token and chat id

Configure it on the **Automation** page, which also has a test button and the recent alerts (with the channels that accepted each one).

### GPU power limits

Each GPU card on the dashboard sets a power limit: Quiet, Balanced, Max, or a slider within the card's allowed range. After a power sweep (Benchmarks) the three presets are the sweep's measured picks (efficient, balanced, max) for that GPU; before that they are 70%, 85% and 100% of the default limit. The value is applied with `nvidia-smi -pl` (as root through the privileged helper), stored in `/etc/llamira/power-limits.json`, and reapplied at boot by `llamira-power.service`. On one test host, 120 W instead of 170 W gave the same generation speed (52.5 vs 52.4 t/s), about 8% slower prompt processing, and a 10 °C lower temperature.

### Playground

A chat box per model. Requests go through the manager to the router, so it also works over the SSH tunnel. Replies stream in and are rendered as Markdown (sanitized; a toggle shows the raw text), a model's thinking is shown in a collapsible block, images can be attached for vision models, and llama.cpp's timings appear under each reply.

- **Sampling:** top-p, top-k, min-p, repeat penalty and seed. Empty fields use the preset's or llama.cpp's default; the values are saved with the chat.
- **Compare:** one prompt to two models side by side, one after the other (with `models-max 1` the router swaps them), with timings under each reply.
- **Embeddings:** embed a few texts with an embedding model and see the dimensions and the cosine similarity of every pair.

The last 20 chats (configurable) are kept **in the browser** (IndexedDB), per host, with their model, system prompt and settings; they can be reopened, renamed and deleted. They are not stored on the manager and are not shared between devices. Attached images over 300 KB are left out of the saved copy.

### Router settings and API key (llama.cpp page)

- **Settings:** `models-max`, idle sleep (`sleep-idle-seconds`) and `models-autoload` are changed in the drop-in through `llamira privileged router-set`. Each change restarts the router, checks health and the model list, and rolls back on failure.
- **API key:** **Require API key** generates `/etc/llamira/router-api-key` (root + `llamira`) and adds `--api-key-file`. After that only `/health` is public. The manager always sends the key. **Every other client (LiteLLM, OpenWebUI, n8n) must be given the key**; the page shows it with instructions.

### Speculative decoding

The preset form has a *Speculative decoding* section: type (from `--help`), draft model (cached or a file path), draft max/min/minimum probability, draft GPUs and layers, and draft KV types. On one test host, the built-in MTP head of `unsloth/Qwen3.5-9B-MTP-GGUF:Q5_K_M` (`spec-type = draft-mtp`) gave 58.0 vs 48.3 t/s generation (+20%). The gain depends on the content.

### Schedules (Automation page)

Rules load a model, unload a model, or unload all models at HH:MM on chosen weekdays. Times are in the browser's time zone and stored per rule, so a host running in UTC is fine. *Run now* executes a rule immediately.

### Prometheus

`GET /metrics` on the UI address reports: router up, model loaded/status, per-GPU memory/utilisation/temperature/power/power limit, disk space on `/models`, and request/token counters per model (counted from the start of the service).

### Fleet

With more than one host in `hosts.yaml`, `connect` shows a **Fleet** page with each host's health, GPUs and loaded models side by side. **Copy a preset to another host** adds a preset section to the other host's working copy (with a warning if its model isn't downloaded there); it goes live there through the normal review & apply.

### Backup & restore (Health page)

**Export** writes one JSON file with the preset file, schedules, alert settings, GPU power limits and the llamira config (for reference). Tokens are redacted unless you tick *include secrets*. **Import** writes schedules, alert settings and power limits directly (redacted secrets keep this host's values), and loads the presets into the working copy for review & apply. Models are not included; presets refer to them by id. The CLI has the same as `llamira export` / `llamira import`.

### Host RAM and CPU

A **Host** card next to the GPU cards (Dashboard and Benchmarks) shows RAM split into llama.cpp / other / free, swap (amber when in use), CPU with a sparkline, load and CPU temperature. CPU-offloaded models (`n-gpu-layers` below the layer count, `cpu-moe`) and mmap'd weights live in system RAM, and a swapping host looks like a slow GPU. Everything comes from procfs and hwmon; llama.cpp's own memory and CPU come from the router unit's cgroup (router + model processes), or from its processes when the cgroup isn't readable. Benchmark results store peak RAM, llama.cpp's peak and the CPU while generating. The Fleet view has one line per host, `/metrics` has `llama_host_*` and `llama_server_*` gauges, alerts can fire on host RAM % and swap in use, and the VRAM estimate warns when the CPU part of a config needs more RAM than is available to llama.cpp.

### GPUs

NVIDIA GPUs are read with `nvidia-smi`. AMD GPUs are read from the amdgpu sysfs interface (VRAM, busy %, temperature, power, clock, fan), with no extra tool. Device names (`CUDA0`, `ROCm0`, `Vulkan0`, …) come from the active binary's `llama-server --list-devices`. Power limits and per-process GPU memory are NVIDIA-only. On AMD APUs only the dedicated VRAM carve-out is reported, not the shared system memory, so VRAM estimates are too pessimistic there. Intel GPUs are not supported.

### Restore, import, export

- **History:** expand an apply and click **Load version before/after this change**.
- **Presets → Backups:** a backup is made before every apply, and you can load any of them.
- **Presets → Import… / Export:** read a local `models.ini`, or download the working copy.

Each of these only replaces the **working copy**. Making it live always goes through Review & apply (validation, dry-run, hot reload, verify, rollback).

### VRAM estimate

The preset form shows a live per-GPU estimate for the form's current, unsaved settings. **Fit max context** searches the estimate for the largest `ctx-size` (in steps of 1024, at most the trained context) that fits on the selected GPUs without being tight, and also offers a `q8_0` KV cache alternative when that buys at least 1.5× the context. Each GPU gets weights, vision projector, KV cache and recurrent state, and runtime overhead, compared against the memory left by other processes. The estimate covers:
- context size and KV cache types
- GPU selection, split mode and tensor split, and partial offload
- `cpu-moe` / `n-cpu-moe`

It follows llama.cpp's own rules:
- layers are assigned to GPUs the way llama.cpp does it, with the output head counted as an extra layer;
- the KV cache is counted only for layers that have one: hybrid models such as Qwen3.5 use every 4th layer, and sliding-window models are handled too;
- the recurrent state uses llama.cpp's formula.

Sizes come from the GGUF tensor table. The Review & apply dialog shows the verdict for every added or changed model. `llamira inspect FILE.gguf` prints the metadata and per-layer sizes the estimate uses.

### Downloading models

The **Models** page has three tabs. **Installed** lists the downloaded models. **Recommended** suggests curated models with the largest quantization that fits this host's GPUs. While nothing is installed it offers a *starter set*: one chat model, one coder and one embedding model. **Search** finds GGUF repos on Hugging Face and lists each quantization with its size and a rough VRAM fit. A bar at the top shows disk use (models, other, free). It turns amber or red near the low-disk alert threshold and previews the size of the download under the pointer.

**Download + preset** (Recommended) also adds a preset once the download finishes: `hf-repo`, the recommended context, the GPU it fits on, and `embeddings = true` for embedding models. The manager does this through the normal apply pipeline, so it happens even if the browser is closed.

A download uses the router's own downloader: `POST /models` with `org/repo:QUANT`, which also fetches a matching mmproj or MTP file. Progress is live, and a download can be cancelled. Downloads land in `LLAMA_CACHE`, and the model appears under its `org/repo:QUANT` ID.

**Delete** (Dashboard or Installed tab) removes a cached model from disk. It is refused while the model is loaded or referenced by a preset.

For gated or private repos, set `hf_token` in `/etc/llamira/config.yaml` so search and repo details work. The router also needs `HF_TOKEN` in its environment for the download, e.g. `Environment=HF_TOKEN=...` in the drop-in.

## Building

Go (version in `go.mod`) and Node 22 (see `.nvmrc`; the Makefile uses nvm when it is installed).

```sh
make build        # web/ (Vite) → web/dist, then a static Go binary at bin/llamira (UI embedded)
make test         # go vet + go test + vue-tsc
make dev          # `connect` on :7070 + Vite dev server with hot reload on :5173
make release      # linux amd64/arm64, darwin arm64, windows amd64 binaries
```

## License

[MIT](LICENSE)
