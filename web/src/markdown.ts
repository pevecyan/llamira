// Markdown rendering for model output. The output is untrusted, so the HTML that markdown-it
// produces is sanitized with DOMPurify before it is used with v-html.
import DOMPurify from 'dompurify'
import MarkdownIt from 'markdown-it'

const md = new MarkdownIt({ html: false, linkify: true, breaks: true })

// fenced code: header with the language and a copy button (handled by event delegation)
md.renderer.rules.fence = (tokens, idx) => {
  const t = tokens[idx]
  const lang = (t.info || '').trim().split(/\s+/)[0]
  const code = md.utils.escapeHtml(t.content)
  return `<div class="md-code"><div class="md-code-h"><span>${md.utils.escapeHtml(lang || 'code')}</span><button type="button" data-copy>copy</button></div><pre><code>${code}</code></pre></div>`
}

DOMPurify.addHook('afterSanitizeAttributes', (node) => {
  if (node.tagName === 'A') {
    node.setAttribute('target', '_blank')
    node.setAttribute('rel', 'noopener noreferrer')
  }
})

export function renderMarkdown(src: string): string {
  return DOMPurify.sanitize(md.render(src), { ADD_ATTR: ['target', 'data-copy'] })
}

// Click handler for a container with rendered markdown: copies code blocks.
export function onMarkdownClick(e: MouseEvent) {
  const btn = (e.target as HTMLElement).closest('[data-copy]') as HTMLElement | null
  if (!btn) return
  const code = btn.closest('.md-code')?.querySelector('code')?.textContent ?? ''
  const done = (ok: boolean) => {
    btn.textContent = ok ? 'copied' : 'copy failed'
    setTimeout(() => (btn.textContent = 'copy'), 1200)
  }
  copyText(code).then(done)
}

// copyText copies to the clipboard. navigator.clipboard only exists on https/localhost, and the
// UI is often plain http on the LAN, so fall back to execCommand('copy').
export async function copyText(text: string): Promise<boolean> {
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      /* fall through */
    }
  }
  return legacyCopy(text)
}

function legacyCopy(text: string): boolean {
  const ta = document.createElement('textarea')
  ta.value = text
  ta.setAttribute('readonly', '')
  ta.style.position = 'fixed'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.select()
  let ok = false
  try {
    ok = document.execCommand('copy')
  } catch {
    ok = false
  }
  ta.remove()
  return ok
}
