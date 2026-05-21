// Mirrors internal/stream/frame.go.

export type FrameKind =
  | 'text.delta'
  | 'thinking'
  | 'tool.use.start'
  | 'tool.use.result'
  | 'todo.update'
  | 'ask.question'
  | 'usage'
  | 'status'
  | 'stop'
  | 'meta'
  | 'hook'
  | 'pty.raw'
  | 'cc.raw'

export interface Frame {
  session: string
  seq: number
  ts: string
  kind: FrameKind
  data?: Record<string, unknown>
}

export interface TodoItem {
  id?: string
  subject: string
  status: 'pending' | 'in_progress' | 'completed' | string
  activeForm?: string
}

export type SessionState =
  | 'starting'
  | 'idle'
  | 'working'
  | 'waiting_for_input'
  | 'stopped'
  | 'failed'

export interface Session {
  id: string
  workdir: string
  model?: string
  auth_mode?: 'subscription' | 'api_key' | string
  state: SessionState
  created_at: string
  started_at?: string
  stopped_at?: string
  exit_code?: number
  last_seq: number
}

export interface TokenPublic {
  id: string
  label: string
  scopes: string[]
  created_at: string
  expires_at?: string
}
