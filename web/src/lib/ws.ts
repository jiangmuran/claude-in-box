import { pushFrame, currentToken } from './stores'
import type { Frame } from './types'

export type ConnectionState = 'idle' | 'connecting' | 'open' | 'closed' | 'error'

export interface FrameStream {
  state(): ConnectionState
  lastSeq(): number
  close(): void
}

interface Options {
  fromSeq?: number
  onState?: (s: ConnectionState) => void
}

// connectFrames opens a WebSocket to /ws/sessions/<id>?from=<seq> using the
// `bearer.<token>` subprotocol for auth. It auto-reconnects with a backoff
// that walks 0.5s → 1s → 2s → 4s and tops out at 8s.
export function connectFrames(sessionId: string, opts: Options = {}): FrameStream {
  let ws: WebSocket | null = null
  let closed = false
  let attempt = 0
  let lastSeq = opts.fromSeq ?? 0
  let st: ConnectionState = 'idle'

  function setState(next: ConnectionState) {
    st = next
    opts.onState?.(next)
  }

  function dial() {
    if (closed) return
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const tok = currentToken()
    const url = `${proto}//${location.host}/ws/sessions/${sessionId}?from=${lastSeq}`
    setState('connecting')
    ws = new WebSocket(url, tok ? [`bearer.${tok}`, 'json'] : ['json'])

    ws.onopen = () => {
      attempt = 0
      setState('open')
    }
    ws.onmessage = (ev) => {
      try {
        const f: Frame = JSON.parse(ev.data)
        if (typeof f.seq === 'number' && f.seq > lastSeq) lastSeq = f.seq
        pushFrame(f)
      } catch {
        /* ignore malformed message */
      }
    }
    ws.onerror = () => {
      setState('error')
    }
    ws.onclose = () => {
      if (closed) { setState('closed'); return }
      setState('closed')
      const delay = Math.min(8000, 500 * Math.pow(2, attempt++))
      setTimeout(dial, delay)
    }
  }

  dial()

  return {
    state: () => st,
    lastSeq: () => lastSeq,
    close: () => {
      closed = true
      ws?.close()
    },
  }
}
