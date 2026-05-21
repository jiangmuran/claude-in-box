<script lang="ts">
  import { T } from '../lib/i18n'

  type View = 'chat' | 'terminal' | 'inspector'

  interface Props {
    active: View
    oninput: (v: View) => void
  }
  let { active, oninput }: Props = $props()

  let tabs = $derived([
    { id: 'chat' as const,       label: $T('driver', '驱动'),    sub: $T('structured', '结构化') },
    { id: 'terminal' as const,   label: $T('terminal', '终端'),   sub: $T('raw pty', '原始 pty') },
    { id: 'inspector' as const,  label: $T('inspector', '检视器'), sub: $T('wire', '线缆') },
  ])
</script>

<nav class="bar" aria-label={$T('view tabs', '视图切换')}>
  {#each tabs as t (t.id)}
    <button
      class="tab"
      class:active={active === t.id}
      onclick={() => oninput(t.id)}
    >
      <span class="lbl">{t.label}</span>
      <span class="sub">{t.sub}</span>
    </button>
  {/each}
  <span class="spacer"></span>
  <span class="badge mono">[ {$T('live', '直播')} ]</span>
</nav>

<style>
  .bar {
    display: flex;
    align-items: stretch;
    gap: 0;
    padding: 0 1rem;
    border-bottom: 1px solid var(--line);
    background: var(--cream);
    overflow-x: auto;
  }
  .tab {
    position: relative;
    padding: 0.6rem 0.85rem 0.55rem;
    color: var(--ink-3);
    border-bottom: 2px solid transparent;
    display: flex;
    flex-direction: column;
    gap: 0.05rem;
    align-items: flex-start;
    transition: color 120ms ease, border-color 120ms ease;
    white-space: nowrap;
  }
  .tab:hover { color: var(--ink); }
  .tab.active {
    color: var(--coral-deep);
    border-bottom-color: var(--coral);
  }
  .lbl {
    font-family: var(--font-mono);
    font-size: 13px;
    letter-spacing: 0.02em;
  }
  .sub {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--ink-faint);
    letter-spacing: 0.16em;
    text-transform: uppercase;
  }
  .spacer { flex: 1; }
  .badge {
    align-self: center;
    color: var(--ok);
    font-size: 10px;
    letter-spacing: 0.16em;
    text-transform: uppercase;
  }
</style>
