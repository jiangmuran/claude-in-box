<script lang="ts">
  import { frames, KINDS } from '../lib/stores'
  import type { FrameKind } from '../lib/types'

  let filter = $state<FrameKind | 'all'>('all')

  let filtered = $derived.by(() => {
    return filter === 'all' ? $frames : $frames.filter((f) => f.kind === filter)
  })

  function fmtData(d: unknown): string {
    if (d == null) return ''
    try { return JSON.stringify(d, null, 2) } catch { return String(d) }
  }
</script>

<div class="wrap">
  <div class="bar">
    <span class="divider">wire</span>
    <div class="chips">
      <button
        class="chip"
        class:active={filter === 'all'}
        onclick={() => (filter = 'all')}
      >all <span class="dim">·{$frames.length}</span></button>
      {#each KINDS as k (k)}
        {@const n = $frames.filter((f) => f.kind === k).length}
        {#if n > 0}
          <button
            class="chip"
            class:active={filter === k}
            onclick={() => (filter = k)}
          >{k} <span class="dim">·{n}</span></button>
        {/if}
      {/each}
    </div>
  </div>

  <ol class="list">
    {#each filtered as f (f.seq)}
      <li class="row">
        <div class="head">
          <span class="seq mono">#{String(f.seq).padStart(5, '0')}</span>
          <span class="kind mono">{f.kind}</span>
          <span class="ts mono">{new Date(f.ts).toLocaleTimeString()}</span>
        </div>
        {#if f.data}
          <details>
            <summary class="mono">payload</summary>
            <pre class="mono">{fmtData(f.data)}</pre>
          </details>
        {/if}
      </li>
    {/each}
    {#if filtered.length === 0}
      <li class="empty mono">— no frames —</li>
    {/if}
  </ol>
</div>

<style>
  .wrap { flex: 1; display: flex; flex-direction: column; min-height: 0; }

  .bar {
    display: flex;
    flex-direction: column;
    gap: 0.6rem;
    padding: 0.75rem 1rem 0.6rem;
    border-bottom: 1px solid var(--line);
    background: var(--cream);
  }
  .chips { display: flex; flex-wrap: wrap; gap: 0.3rem; }
  .chip {
    padding: 0.25rem 0.55rem;
    font-family: var(--font-mono);
    font-size: 11px;
    border: 1px solid var(--line);
    color: var(--ink-3);
    background: var(--cream);
    transition: color 100ms ease, border-color 100ms ease;
    border-radius: var(--r-xs);
  }
  .chip:hover { color: var(--ink); border-color: var(--line-strong); }
  .chip.active { color: var(--coral-deep); border-color: var(--coral); background: rgba(217,119,87,0.08); }
  .dim { color: var(--ink-faint); }

  .list {
    flex: 1;
    overflow-y: auto;
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .row {
    border-bottom: 1px dotted var(--line);
    padding: 0.5rem 1rem;
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
  }
  .head {
    display: grid;
    grid-template-columns: 6rem 1fr auto;
    gap: 0.5rem;
    align-items: baseline;
  }
  .seq { color: var(--ink-faint); }
  .kind { color: var(--coral-dark); }
  .ts { color: var(--ink-faint); font-size: 11px; }

  details summary {
    list-style: none;
    cursor: pointer;
    font-size: 11px;
    color: var(--ink-3);
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }
  details summary::-webkit-details-marker { display: none; }
  details summary::before { content: '▸ payload'; }
  details[open] summary::before { content: '▾ payload'; }

  details pre {
    margin: 0.4rem 0 0;
    padding: 0.5rem 0.65rem;
    background: var(--cream-2);
    border: 1px solid var(--line);
    font-size: 0.75rem;
    line-height: 1.55;
    color: var(--ink);
    max-height: 16rem;
    overflow: auto;
    white-space: pre-wrap;
    word-break: break-word;
  }

  .empty {
    padding: 1.5rem;
    text-align: center;
    color: var(--ink-faint);
    font-size: 11px;
    letter-spacing: 0.08em;
  }
</style>
