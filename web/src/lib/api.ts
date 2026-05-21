import { authToken } from './stores'
import { get } from 'svelte/store'
import type {
  Session, TokenPublic, Frame, ClaudeAuthStatus, ClaudeFlowSnapshot,
  ShellView, FSListResponse, FSReadResponse,
  Provider, ProviderProbe, Prefs,
} from './types'

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
    provider_id?: string
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

  // shells (plain-bash PTYs)
  listShells:   () => req<{ shells: ShellView[] }>('GET', '/api/shells'),
  createShell:  (opts: { cwd?: string; cmd?: string; args?: string[]; cols?: number; rows?: number } = {}) =>
    req<ShellView>('POST', '/api/shells', opts),
  getShell:     (id: string) => req<ShellView>('GET', `/api/shells/${id}`),
  killShell:    (id: string) => req<void>('DELETE', `/api/shells/${id}`),
  shellResize:  (id: string, cols: number, rows: number) =>
    req<void>('POST', `/api/shells/${id}/resize`, { cols, rows }),

  // files (constrained file browser/editor)
  fsRoots: () => req<{ roots: string[] }>('GET', '/api/fs/roots'),
  fsList:  (root: string, path: string) =>
    req<FSListResponse>('GET', `/api/fs/list?root=${encodeURIComponent(root)}&path=${encodeURIComponent(path)}`),
  fsRead:  (root: string, path: string) =>
    req<FSReadResponse>('GET', `/api/fs/read?root=${encodeURIComponent(root)}&path=${encodeURIComponent(path)}`),
  fsWrite: (root: string, path: string, content: string) =>
    req<void>('PUT', '/api/fs/write', { root, path, content }),
  fsMkdir: (root: string, path: string) =>
    req<void>('POST', '/api/fs/mkdir', { root, path }),
  fsDelete: (root: string, path: string) =>
    req<void>('DELETE', `/api/fs/delete?root=${encodeURIComponent(root)}&path=${encodeURIComponent(path)}`),

  // providers (third-party Anthropic-compatible endpoints)
  listProviders:   () => req<{ providers: Provider[] }>('GET', '/api/providers'),
  addProvider:     (p: { label: string; api_host: string; api_key: string; model?: string }) =>
    req<Provider>('POST', '/api/providers', p),
  replaceProvider: (id: string, p: { label: string; api_host: string; api_key: string; model?: string }) =>
    req<Provider>('PUT', `/api/providers/${id}`, p),
  deleteProvider:  (id: string) => req<void>('DELETE', `/api/providers/${id}`),
  probeProvider:   (body: { id?: string; label?: string; api_host?: string; api_key?: string; model?: string }) =>
    req<ProviderProbe>('POST', '/api/providers/probe', body),

  // preferences
  getPrefs:   () => req<Prefs>('GET', '/api/prefs'),
  patchPrefs: (p: Prefs) => req<Prefs>('PATCH', '/api/prefs', p),

  // claude auth (in-container `claude auth login`)
  claudeStatus: () => req<ClaudeAuthStatus>('GET', '/api/auth/claude/status'),
  claudeStart:  (opts: { sso?: boolean; console?: boolean; email?: string } = {}) =>
    req<ClaudeFlowSnapshot>('POST', '/api/auth/claude/start', opts),
  claudeCode:   (flowId: string, code: string) =>
    req<ClaudeFlowSnapshot>('POST', '/api/auth/claude/code', { flow_id: flowId, code }),
  claudeCancel: (flowId: string) =>
    req<void>('POST', '/api/auth/claude/cancel', { flow_id: flowId }),
  claudeLogout: () => req<void>('POST', '/api/auth/claude/logout'),
}
