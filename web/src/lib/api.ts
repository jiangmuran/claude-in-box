import { authToken } from './stores'
import { get } from 'svelte/store'
import type { Session, TokenPublic, Frame } from './types'

function authHeader(): HeadersInit {
  const t = get(authToken)
  return t ? { Authorization: `Bearer ${t}` } : {}
}

async function req<T>(
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
  const init: RequestInit = {
    method,
    headers: {
      ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}),
      ...authHeader(),
    },
  }
  if (body !== undefined) init.body = JSON.stringify(body)

  const res = await fetch(path, init)
  if (res.status === 204) return undefined as T
  const text = await res.text()
  let data: unknown = undefined
  try {
    data = text ? JSON.parse(text) : undefined
  } catch {
    data = { raw: text }
  }
  if (!res.ok) {
    const msg =
      (data as { error?: string })?.error ?? `HTTP ${res.status}`
    throw new ApiError(msg, res.status, data)
  }
  return data as T
}

export class ApiError extends Error {
  status: number
  data: unknown
  constructor(msg: string, status: number, data: unknown) {
    super(msg)
    this.status = status
    this.data = data
  }
}

export const api = {
  health: () => req<{ status: string; version: string; commit: string; mode: string }>('GET', '/api/health'),

  // sessions
  listSessions: () => req<{ sessions: Session[] }>('GET', '/api/sessions'),
  getSession:   (id: string) => req<Session>('GET', `/api/sessions/${id}`),
  createSession: (opts: {
    workdir?: string
    model?: string
    auth_mode?: 'subscription' | 'api_key'
    api_key?: string
    oauth_token?: string
    resume_from?: string
    bypass_permissions?: boolean
  }) => req<Session>('POST', '/api/sessions', opts),
  killSession:   (id: string, signal: 'term' | 'kill' = 'term') =>
    req<Session>('DELETE', `/api/sessions/${id}?signal=${signal}`),
  sendInput:     (id: string, data: string) =>
    req<{ bytes: number }>('POST', `/api/sessions/${id}/input`, { data }),
  setModel:      (id: string, model: string) =>
    req<{ id: string; model: string }>('POST', `/api/sessions/${id}/model`, { model }),
  interrupt:     (id: string) =>
    req<{ id: string }>('POST', `/api/sessions/${id}/interrupt`),
  transcript:    (id: string, fromSeq = 0) =>
    req<{ id: string; last_seq: number; frames: Frame[] }>(
      'GET',
      `/api/sessions/${id}/transcript${fromSeq ? `?from=${fromSeq}` : ''}`,
    ),

  // tokens
  listTokens:   () => req<{ tokens: TokenPublic[] }>('GET', '/api/tokens'),
  mintToken:    (label: string, scopes: string[], ttlHours = 0) =>
    req<{ token: TokenPublic; plaintext: string }>('POST', '/api/tokens', {
      label, scopes, ttl_hours: ttlHours,
    }),
  revokeToken:  (id: string) => req<void>('DELETE', `/api/tokens/${id}`),
}
