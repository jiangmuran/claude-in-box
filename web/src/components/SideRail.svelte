<script lang="ts">
  import { todos, usage, status, frames, sessions, activeSessionId } from '../lib/stores'
  import { T } from '../lib/i18n'
  import type { TodoItem } from '../lib/types'

  type ToolCall = { seq: number; tool: string; durationMs?: number; error?: string; ok: boolean }
  let toolCalls = $derived.by((): ToolCall[] => {
    const map = new Map<string, ToolCall>()
    const seen: ToolCall[] = []
    for (const f of $frames) {
      if (f.kind === 'tool.use.start') {
        const d = f.data as { tool_use_id?: string; tool?: string }
        const tc: ToolCall = { seq: f.seq, tool: d?.tool ?? 'tool', ok: false }
        seen.push(tc)
        if (d?.tool_use_id) map.set(d.tool_use_id, tc)
      } else if (f.kind === 'tool.use.result') {
        const d = f.data as { tool_use_id?: string; tool?: string; error?: string; duration_ms?: number }
        const tc = (d?.tool_use_id && map.get(d.tool_use_id)) || seen[seen.length - 1]
        if (tc) {
          tc.durationMs = d?.duration_ms
          tc.error = d?.error
          tc.ok = !d?.error
        }
      }
    }
    return seen.slice(-12).reverse()
  })

  let u = $derived($usage as { input?: number; output?: number; cache_read?: number; cache_write?: number } | null)
  let st = $derived($status)

  // Aggregate token totals across all usage frames in the session, plus
  // the latest meta frame for live model name (the session.Model is set
  // at create; claude /model command updates this meta frame).
  type Totals = { in: number; out: number; cacheRead: number; calls: number }
  let totals = $derived.by((): Totals => {
    const t: Totals = { in: 0, out: 0, cacheRead: 0, calls: 0 }
    for (const f of $frames) {
      if (f.kind === 'usage') {
        const d = f.data as { input?: number; output?: number; cache_read?: number }
        t.in        += d?.input ?? 0
        t.out       += d?.output ?? 0
        t.cacheRead += d?.cache_read ?? 0
      } else if (f.kind === 'tool.use.start') {
        t.calls += 1
      }
    }
    return t
  })

  let activeSess = $derived($sessions.find((s) => s.id === $activeSessionId) || null)
  let liveModel = $derived.by((): string => {
    for (let i = $frames.length - 1; i >= 0; i--) {
      const f = $frames[i]
      if (f.kind !== 'meta') continue
      const d = f.data as { model?: string }
      if (d?.model) return d.model
    }
    return (activeSess?.model as string) || ''
  })

  function badgeColor(s?: TodoItem['status']) {
    if (s === 'completed') return 'tick-done'
    if (s === 'in_progress') return 'tick-doing'
    return 'tick-todo'
  }
</script>

<div class="rail-inner">
  <section class="monitor">
    <span class="divider">{$T('monitor', '运行状态')}</span>
    <dl class="kv">
      <dt>{$T('model', '模型')}</dt>
      <dd class="mono">{liveModel || '—'}</dd>
      <dt>{$T('effort', '思考深度')}</dt>
      <dd class="mono">{(activeSess as { effort?: string })?.effort || $T('auto', '自动')}</dd>
      <dt>{$T('tokens · in', '输入')}</dt>
      <dd class="mono">{totals.in}</dd>
      <dt>{$T('tokens · out', '输出')}</dt>
      <dd class="mono">{totals.out}</dd>
      {#if totals.cacheRead}
        <dt>{$T('tokens · cache', '缓存')}</dt>
        <dd class="mono">{totals.cacheRead}</dd>
      {/if}
      <dt>{$T('tool calls', '工具调用')}</dt>
      <dd class="mono">{totals.calls}</dd>
    </dl>
  </section>

  <section>
    <span class="divider">{$T('todos', '待办')}</span>
    <ul class="todos">
      {#each $todos as t, i (i)}
        <li>
          <span class={'tick ' + badgeColor(t.status)} aria-hidden="true"></span>
          <span class="t-text">{t.status === 'in_progress' && t.activeForm ? t.activeForm : t.subject}</span>
        </li>
      {/each}
      {#if $todos.length === 0}
        <li class="empty mono">{$T('— none yet —', '— 暂无 —')}</li>
      {/if}
    </ul>
  </section>

  <section>
    <span class="divider">{$T('tool calls', '工具调用')}</span>
    <ul class="tools">
      {#each toolCalls as tc (tc.seq)}
        <li class:err={tc.error}>
          <span class="dot" class:ok={tc.ok} class:err={tc.error}></span>
          <span class="t-name mono">{tc.tool}</span>
          {#if tc.durationMs != null}<span class="t-dur mono">{tc.durationMs}ms</span>{/if}
        </li>
      {/each}
      {#if toolCalls.length === 0}
        <li class="empty mono">{$T('— none yet —', '— 暂无 —')}</li>
      {/if}
    </ul>
  </section>

  <section>
    <span class="divider">{$T('usage', '用量')}</span>
    {#if u}
      <dl class="usage">
        <dt>{$T('in', '入')}</dt><dd class="mono">{u.input ?? 0}</dd>
        <dt>{$T('out', '出')}</dt><dd class="mono">{u.output ?? 0}</dd>
        {#if u.cache_read != null}<dt>{$T('cache', '缓存')}</dt><dd class="mono">{u.cache_read}</dd>{/if}
      </dl>
    {:else}
      <p class="empty mono">{$T('— waiting for first turn —', '— 等待第一轮 —')}</p>
    {/if}
  </section>

  <section>
    <span class="divider">{$T('status', '状态')}</span>
    {#if st}
      <p class="status mono">[ {String(st.state).replace(/_/g, ' ')} ]</p>
      {#if st.elapsed_ms != null}<p class="elapsed mono">{$T('elapsed', '已用')} · {Math.round(st.elapsed_ms / 1000)}s</p>{/if}
    {:else}
      <p class="empty mono">{$T('— idle —', '— 空闲 —')}</p>
    {/if}
  </section>
</div>

<style>
  .monitor .kv {
    margin: 0;
    display: grid;
    grid-template-columns: auto 1fr;
    column-gap: 0.75rem;
    row-gap: 0.25rem;
    font-size: 12px;
  }
  .monitor .kv dt { color: var(--ink-3); font-family: var(--font-display); }
  .monitor .kv dd { margin: 0; color: var(--ink); text-align: right; word-break: break-all; }
  .rail-inner {
    display: flex;
    flex-direction: column;
    gap: 1.5rem;
  }
  section { display: flex; flex-direction: column; gap: 0.6rem; }

  .todos, .tools, .usage {
    list-style: none;
    padding: 0;
    margin: 0;
    display: flex;
    flex-direction: column;
    gap: 0.45rem;
  }

  .todos li {
    display: grid;
    grid-template-columns: 1.1rem 1fr;
    gap: 0.5rem;
    align-items: baseline;
    font-family: var(--font-display);
    font-variation-settings: 'opsz' 14, 'SOFT' 80;
    font-size: 0.95rem;
    color: var(--ink-2);
    line-height: 1.45;
  }
  .tick {
    width: 12px; height: 12px;
    border: 1px solid var(--ink-3);
    border-radius: 2px;
    display: inline-block;
    margin-top: 0.3em;
    position: relative;
  }
  .tick-doing { border-color: var(--amber); background: rgba(255,183,107,0.35); animation: pulse 1.2s ease-in-out infinite; }
  .tick-done {
    background: var(--coral);
    border-color: var(--coral);
  }
  .tick-done::after {
    content: '';
    position: absolute;
    inset: 0;
    border: solid var(--cream);
    border-width: 0 1.6px 1.6px 0;
    transform: rotate(45deg) translate(-1px, -2px) scale(0.55);
    transform-origin: center;
  }

  .tools li {
    display: grid;
    grid-template-columns: 0.7rem auto auto 1fr;
    gap: 0.5rem;
    align-items: baseline;
    font-size: 12px;
  }
  .tools .dot {
    width: 6px; height: 6px;
    border-radius: 50%;
    background: var(--ink-faint);
    margin-top: 0.45em;
  }
  .tools .dot.ok  { background: var(--ok); }
  .tools .dot.err { background: var(--danger); }
  .tools .t-dur { color: var(--ink-faint); justify-self: end; }
  .tools li.err .t-name { color: var(--danger); }

  .usage {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 0.2rem 0.75rem;
  }
  .usage dt {
    font-family: var(--font-mono);
    font-size: 10px;
    letter-spacing: 0.16em;
    text-transform: uppercase;
    color: var(--ink-3);
  }
  .usage dt::before { content: '['; opacity: 0.5; padding-right: 4px; }
  .usage dt::after  { content: ']'; opacity: 0.5; padding-left: 4px; }
  .usage dd { margin: 0; font-size: 0.95rem; color: var(--ink); }

  .status { color: var(--coral-deep); font-size: 12px; margin: 0; }
  .elapsed { color: var(--ink-faint); font-size: 11px; margin: 0; }

  .empty {
    color: var(--ink-faint);
    font-size: 11px;
    letter-spacing: 0.05em;
  }
</style>
