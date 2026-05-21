<script lang="ts">
  import { T } from '../lib/i18n'
  import type { Session } from '../lib/types'

  interface Props {
    sessions: Session[]
    activeId: string
    onselect: (id: string) => void
  }
  let { sessions, activeId, onselect }: Props = $props()

  function shortId(id: string) { return id.slice(0, 8) }
  function relative(ts?: string) {
    if (!ts) return ''
    const d = new Date(ts).getTime()
    const s = Math.floor((Date.now() - d) / 1000)
    if (s < 60) return `${s}s`
    if (s < 3600) return `${Math.floor(s / 60)}m`
    if (s < 86400) return `${Math.floor(s / 3600)}h`
    return `${Math.floor(s / 86400)}d`
  }
</script>

<ul class="list">
  {#each sessions as s (s.id)}
    <li>
      <button
        class="row"
        class:active={s.id === activeId}
        onclick={() => onselect(s.id)}
      >
        <span class="dot dot-{s.state}" aria-hidden="true"></span>
        <span class="id mono">{shortId(s.id)}</span>
        <span class="model mono">{s.model || 'default'}</span>
        <span class="rel mono">{relative(s.created_at)}</span>
      </button>
    </li>
  {/each}
  {#if sessions.length === 0}
    <li class="empty">{$T('— no sessions —', '— 暂无会话 —')}</li>
  {/if}
</ul>

<style>
  .list {
    list-style: none;
    padding: 0;
    margin: 0;
    display: flex;
    flex-direction: column;
    gap: 0.15rem;
  }
  .row {
    display: grid;
    grid-template-columns: 0.65rem 4.5rem minmax(0, 1fr) auto;
    align-items: center;
    gap: 0.55rem;
    padding: 0.45rem 0.6rem;
    font-family: var(--font-mono);
    font-size: 12px;
    width: 100%;
    text-align: left;
    color: var(--ink);
    border-left: 2px solid transparent;
    transition: background 120ms ease, color 120ms ease, border-color 120ms ease;
  }
  .row:hover { background: var(--cream-2); }
  .row.active {
    background: var(--cream-2);
    border-left-color: var(--coral);
    color: var(--coral-deep);
  }

  .dot {
    width: 8px; height: 8px;
    border-radius: 50%;
    background: var(--ink-faint);
    box-shadow: 0 0 0 2px var(--cream);
  }
  .dot-working { background: var(--amber); animation: pulse 1.2s ease-in-out infinite; }
  .dot-idle    { background: var(--ok); }
  .dot-waiting_for_input { background: var(--amber); }
  .dot-stopped { background: var(--ink-3); }
  .dot-failed  { background: var(--danger); }
  .dot-starting { background: var(--coral); animation: pulse 1s ease-in-out infinite; }

  .id    { letter-spacing: 0.04em; }
  .model { color: var(--ink-3); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .rel   { color: var(--ink-faint); font-size: 11px; }

  .empty {
    padding: 1rem;
    color: var(--ink-faint);
    font-family: var(--font-mono);
    font-size: 11px;
    text-align: center;
    letter-spacing: 0.04em;
  }
</style>
