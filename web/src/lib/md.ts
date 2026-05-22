// Tiny Markdown → HTML helper for chat bubbles. Uses `marked` for parsing,
// `dompurify` to scrub anything weird before we inject into the DOM.

import { marked } from 'marked'
import DOMPurify from 'dompurify'

// Configure once. GFM tables + breaks (treat a single newline as <br>) match
// claude's typical assistant output.
marked.setOptions({ gfm: true, breaks: true })

/**
 * mdToHtml converts a Markdown string to sanitized HTML. Returns "" when
 * input is empty/non-string.
 */
export function mdToHtml(src: string): string {
  if (!src) return ''
  try {
    const raw = marked.parse(src, { async: false }) as string
    return DOMPurify.sanitize(raw, {
      ADD_ATTR: ['target', 'rel'],
    })
  } catch {
    return escapeText(src)
  }
}

/**
 * escapeText falls back to plain-text-with-<br> if markdown parsing fails.
 */
export function escapeText(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/\n/g, '<br>')
}
