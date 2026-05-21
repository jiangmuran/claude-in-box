import { writable, derived, get } from 'svelte/store'
import type { Session, Frame, FrameKind, TodoItem } from './types'

// ---- auth ----

const TOKEN_KEY = 'cib.token'

function readToken(): string {
  try { return localStorage.getItem(TOKEN_KEY) || '' } catch { return '' }
}

export const authToken = writable<string>(readToken())
authToken.subscribe((v) => {
  try { v ? localStorage.setItem(TOKEN_KEY, v) : localStorage.removeItem(TOKEN_KEY) } catch {}
})

// ---- sessions list ----

export const sessions = writable<Session[]>([])

// ---- active session ----

export const activeSessionId = writable<string>('')

// ---- frame stream for the active session ----

export const frames = writable<Frame[]>([])

// Derived: just the structured frames we render in the chat view.
export const chatFrames = derived(frames, ($frames) =>
  $frames.filter((f) => (
    f.kind === 'text.delta' ||
    f.kind === 'tool.use.start' ||
    f.kind === 'tool.use.result' ||
    f.kind === 'ask.question' ||
    f.kind === 'meta'
  )),
)

// Derived: the current todo list (last `todo.update` wins).
export const todos = derived(frames, ($frames): TodoItem[] => {
  for (let i = $frames.length - 1; i >= 0; i--) {
    if ($frames[i].kind === 'todo.update') {
      const items = ($frames[i].data as { items?: TodoItem[] })?.items
      return items ?? []
    }
  }
  return []
})

// Derived: the latest usage frame (running totals from CC).
export const usage = derived(frames, ($frames) => {
  for (let i = $frames.length - 1; i >= 0; i--) {
    if ($frames[i].kind === 'usage') return $frames[i].data
  }
  return null
})

// Derived: latest status.
export const status = derived(frames, ($frames) => {
  for (let i = $frames.length - 1; i >= 0; i--) {
    if ($frames[i].kind === 'status') {
      return ($frames[i].data as { state?: string; elapsed_ms?: number })
    }
  }
  return null
})

// ---- helpers ----

export function pushFrame(f: Frame) {
  frames.update((cur) => {
    // Keep a bounded buffer to avoid runaway memory on long sessions.
    const next = cur.concat([f])
    if (next.length > 4096) next.splice(0, next.length - 4096)
    return next
  })
}

export function resetSessionState() {
  frames.set([])
}

export function logout() {
  authToken.set('')
  sessions.set([])
  resetSessionState()
  activeSessionId.set('')
}

export function currentToken(): string {
  return get(authToken)
}

export const KINDS: FrameKind[] = [
  'text.delta',
  'thinking',
  'tool.use.start',
  'tool.use.result',
  'todo.update',
  'ask.question',
  'usage',
  'status',
  'stop',
  'meta',
  'hook',
  'pty.raw',
  'cc.raw',
]
