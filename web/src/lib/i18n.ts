// Tiny i18n built on Svelte stores. Two languages: en / zh.
//
// Usage in a component:
//
//   <script>
//     import { T } from '../lib/i18n'
//   </script>
//
//   <h1>{$T('Hello', '你好')}</h1>
//
// `$T` is the Svelte auto-subscription on a derived store, so any change
// to `lang` re-renders the template. For action handlers, import `lang`
// and read its value via `get(lang)` or use the imperative `tNow()`.

import { writable, derived, get } from 'svelte/store'

export type Lang = 'en' | 'zh'

const KEY = 'cib.lang'

function readLang(): Lang {
  try {
    const v = localStorage.getItem(KEY)
    if (v === 'zh' || v === 'en') return v
    if (typeof navigator !== 'undefined') {
      const bl = (navigator.language || '').toLowerCase()
      if (bl.startsWith('zh')) return 'zh'
    }
  } catch { /* ignore */ }
  return 'en'
}

export const lang = writable<Lang>(readLang())

lang.subscribe((v) => {
  try { localStorage.setItem(KEY, v) } catch { /* ignore */ }
  if (typeof document !== 'undefined') {
    document.documentElement.lang = v === 'zh' ? 'zh-CN' : 'en'
  }
})

export function setLang(l: Lang) { lang.set(l) }
export function toggleLang() { lang.update((l) => (l === 'zh' ? 'en' : 'zh')) }

/**
 * `T` is a derived store whose value is the translation helper for the
 * current language. Use as `$T(en, zh)` inside Svelte templates.
 */
export const T = derived(
  lang,
  ($lang) => (en: string, zh: string) => ($lang === 'zh' ? zh : en),
)

/** Imperative helper for use inside non-reactive contexts (event handlers,
 * effects, network code). */
export function tNow(en: string, zh: string): string {
  return get(lang) === 'zh' ? zh : en
}
