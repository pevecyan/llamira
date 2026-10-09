// Streaming chat completions through the manager (/api/hosts/{host}/chat → router), shared by
// the Playground chat and compare views.

export interface Timings {
  prompt_n: number
  prompt_per_second: number
  predicted_n: number
  predicted_per_second: number
}

export interface Reply {
  content: string
  reasoning?: string
  timings?: Timings
  error?: string
  finish?: string
}

// Sampling options; null/undefined = not sent (the preset's or llama.cpp's default applies).
export interface Sampling {
  top_p?: number | null
  top_k?: number | null
  min_p?: number | null
  repeat_penalty?: number | null
  seed?: number | null
}

export const SAMPLING_FIELDS: { key: keyof Sampling; label: string; step: number; min: number; max?: number; hint: string }[] = [
  { key: 'top_p', label: 'Top-p', step: 0.05, min: 0, max: 1, hint: 'llama.cpp default 0.95' },
  { key: 'top_k', label: 'Top-k', step: 1, min: 0, hint: 'default 40, 0 = off' },
  { key: 'min_p', label: 'Min-p', step: 0.01, min: 0, max: 1, hint: 'default 0.05' },
  { key: 'repeat_penalty', label: 'Repeat penalty', step: 0.05, min: 0, max: 2, hint: 'default 1.0 = off' },
  { key: 'seed', label: 'Seed', step: 1, min: -1, hint: 'empty = random' },
]

export function samplingBody(s: Sampling): Record<string, number> {
  const out: Record<string, number> = {}
  for (const f of SAMPLING_FIELDS) {
    const v = s[f.key]
    if (v !== null && v !== undefined && !Number.isNaN(v)) out[f.key] = v
  }
  return out
}

async function errorOf(res: Response): Promise<string> {
  const t = await res.text()
  try {
    const j = JSON.parse(t)
    return j.error?.message ?? j.error ?? t
  } catch {
    return t || res.statusText
  }
}

// streamChat posts a streaming request and updates r (a reactive object) as deltas arrive.
export async function streamChat(base: string, body: Record<string, unknown>, r: Reply, signal: AbortSignal, onDelta?: () => void) {
  try {
    const res = await fetch(`${base}/chat`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ...body, stream: true }),
      signal,
    })
    if (!res.ok || !res.body) throw new Error(await errorOf(res))
    const reader = res.body.getReader()
    const dec = new TextDecoder()
    let buf = ''
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      buf += dec.decode(value, { stream: true })
      let i
      while ((i = buf.indexOf('\n')) >= 0) {
        const line = buf.slice(0, i).trim()
        buf = buf.slice(i + 1)
        if (!line.startsWith('data:')) continue
        const data = line.slice(5).trim()
        if (data === '[DONE]') continue
        try {
          const ev = JSON.parse(data)
          const d = ev.choices?.[0]?.delta ?? {}
          if (d.reasoning_content) r.reasoning = (r.reasoning ?? '') + d.reasoning_content
          if (d.content) r.content += d.content
          if (ev.timings) r.timings = ev.timings
          if (ev.choices?.[0]?.finish_reason) r.finish = ev.choices[0].finish_reason
          if (ev.error) r.error = ev.error.message ?? String(ev.error)
        } catch {
          /* partial line */
        }
      }
      onDelta?.()
    }
  } catch (e: any) {
    if (e.name !== 'AbortError') r.error = e.message
  }
}

// embed returns one vector per input text.
export async function embed(base: string, model: string, input: string[], signal?: AbortSignal): Promise<number[][]> {
  const res = await fetch(`${base}/embeddings`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ model, input }),
    signal,
  })
  if (!res.ok) throw new Error(await errorOf(res))
  const j = await res.json()
  return (j.data as { index: number; embedding: number[] }[]).sort((a, b) => a.index - b.index).map((d) => d.embedding)
}

export function cosine(a: number[], b: number[]): number {
  let dot = 0
  let na = 0
  let nb = 0
  for (let i = 0; i < Math.min(a.length, b.length); i++) {
    dot += a[i] * b[i]
    na += a[i] * a[i]
    nb += b[i] * b[i]
  }
  return na && nb ? dot / Math.sqrt(na * nb) : 0
}
