<script lang="ts">
  import { todos, usage, status, frames } from '../lib/stores'
  import type { Frame, TodoItem } from '../lib/types'

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

  function badgeColor(s?: TodoItem['status']) {
    if (s === 'completed') return 'tick-done'
    if (s === 'in_progress') return 'tick-doing'
    return 'tick-todo'
  }
</script>

<div class="rail-inner">
  <section>
    <span class="divider">todos</span>
    <ul class="todos">
      {#each $todos as t, i (i)}
        <li>
          <span class={'tick ' + badgeColor(t.status)} aria-hidden="true"></span>
          <span class="t-text">{t.status === 'in_progress' && t.activeForm ? t.activeForm : t.subject}</span>
        </li>
      {/each}
      {#if $todos.length === 0}
        <li class="empty mono">— none yet —</li>
      {/if}
    </ul>
  </section>

  <section>
    <span class="divider">tool calls</span>
    <ul class="tools">
      {#each toolCalls as tc (tc.seq)}
        <li class:err={tc.error}>
          <span class="dot" class:ok={tc.ok} class:err={tc.error}></span>
          <span class="t-name mono">{tc.tool}</span>
          {#if tc.durationMs != null}<span class="t-dur mono">{tc.durationMs}ms</span>{/if}
        </li>
      {/each}
      {#if toolCalls.length === 0}
        <li class="empty mono">— none yet —</li>
      {/if}
    </ul>
  </section>

  <section>
    <span class="divider">usage</span>
    {#if u}
      <dl class="usage">
        <dt>in</dt><dd class="mono">{u.input ?? 0}</dd>
        <dt>out</dt><dd class="mono">{u.output ?? 0}</dd>
        {#if u.cache_read != null}<dt>cache</dt><dd class="mono">{u.cache_read}</dd>{/if}
      </dl>
    {:else}
      <p class="empty mono">— waiting for first turn —</p>
    {/if}
  </section>

  <section>
    <span class="divider">status</span>
    {#if st}
      <p class="status mono">[ {String(st.state).replace(/_/g, ' ')} ]</p>
      {#if st.elapsed_ms != null}<p class="elapsed mono">elapsed · {Math.round(st.elapsed_ms / 1000)}s</p>{/if}
    {:else}
      <p class="empty mono">— idle —</p>
    {/if}
  </section>
</div>

<style>
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
