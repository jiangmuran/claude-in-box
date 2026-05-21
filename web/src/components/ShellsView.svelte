<script lang="ts">
  import { onMount } from 'svelte'
  import { api, ApiError } from '../lib/api'
  import { T } from '../lib/i18n'
  import type { ShellView } from '../lib/types'
  import ShellTerminal from './ShellTerminal.svelte'

  let shells = $state<ShellView[]>([])
  let activeId = $state('')
  let error = $state('')
  let creating = $state(false)
  let mobileOpen = $state(false)

  async function refresh() {
    try {
      const r = await api.listShells()
      shells = r.shells
      if (!activeId && shells.length > 0) activeId = shells[0].id
    } catch (e) {
      error = e instanceof ApiError ? e.message : (e as Error).message
    }
  }

  async function spawnNew() {
    creating = true
    try {
      const s = await api.createShell({ cwd: '/workspace' })
      shells = [s, ...shells]
      activeId = s.id
      mobileOpen = false
    } catch (e) {
      error = e instanceof ApiError ? e.message : (e as Error).message
    } finally {
      creating = false
    }
  }

  async function kill(id: string) {
    try {
      await api.killShell(id)
      shells = shells.filter((s) => s.id !== id)
      if (activeId === id) activeId = shells[0]?.id || ''
    } catch (e) {
      // ignore
    }
  }

  function selectShell(id: string) {
    activeId = id
    mobileOpen = false
  }

  onMount(refresh)

  let active = $derived(shells.find((s) => s.id === activeId) || null)
</script>

<div class="layout" class:open={mobileOpen}>
  <aside class="side">
    <div class="head">
      <span class="divider">{$T('shells', '终端')}</span>
      <button class="new" onclick={spawnNew} disabled={creating}>
        <span class="plus">+</span><span>{creating ? $T('spawning…', '启动中…') : $T('new shell', '新建终端')}</span>
      </button>
    </div>

    {#if error}<p class="err mono">[ {error} ]</p>{/if}

    <ul class="list">
      {#each shells as s (s.id)}
        <li>
          <button
            class="row"
            class:active={s.id === activeId}
            onclick={() => selectShell(s.id)}
          >
            <span class="dot" class:on={s.running}></span>
            <span class="id mono">{s.id.slice(0, 8)}</span>
            <span class="cwd mono">{s.cwd}</span>
          </button>
          <button class="kill" onclick={() => kill(s.id)} title={$T('kill', '杀掉')}>×</button>
        </li>
      {/each}
      {#if shells.length === 0}
        <li class="empty mono">{$T('— no shells —', '— 暂无终端 —')}</li>
      {/if}
    </ul>
  </aside>

  <main class="main">
    <header class="top">
      <button class="hamburger" onclick={() => (mobileOpen = !mobileOpen)} aria-label={$T('toggle shells', '切换终端列表')}>
        <span></span><span></span><span></span>
      </button>
      <div class="crumbs mono">
        {#if active}
          <span class="ink-3">{$T('shell', '终端')}</span>
          <span class="sep">/</span>
          <span class="id">{active.id.slice(0, 8)}</span>
          <span class="sep">·</span>
          <span class="ink-3">{active.cwd}</span>
        {:else}
          <span class="ink-3">{$T('no shell', '没有终端')}</span>
        {/if}
      </div>
    </header>

    {#if active}
      {#key active.id}
        <ShellTerminal shellId={active.id} />
      {/key}
    {:else}
      <section class="empty-area">
        <div class="empty-card">
          <span class="divider">{$T('empty', '空')}</span>
          <h2 class="serif">{$T('no shell yet', '还没有终端')}</h2>
          <p class="ink-3">
            {$T('Spawn a bash and the panel will fill in.', '开一个 bash,这里就会填满。')}
            <br />
            <button class="ghost-link" onclick={spawnNew}>{$T('new shell', '新建终端')}</button>.
          </p>
        </div>
      </section>
    {/if}
  </main>
</div>

<style>
  .layout {
    display: grid;
    grid-template-columns: 280px minmax(0, 1fr);
    height: 100%;
    min-height: 0;
  }
  .side {
    border-right: 1px solid var(--line);
    padding: 1rem;
    display: flex;
    flex-direction: column;
    gap: 0.85rem;
    overflow-y: auto;
  }
  .head { display: flex; flex-direction: column; gap: 0.65rem; }
  .new {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.55rem 0.75rem;
    border: 1px dashed var(--coral);
    color: var(--coral-dark);
    font-family: var(--font-mono);
    font-size: 12px;
    letter-spacing: 0.04em;
    background: transparent;
    transition: background 120ms ease, color 120ms ease;
  }
  .new:hover:not(:disabled) { background: var(--coral); color: var(--cream); border-style: solid; }
  .new:disabled { opacity: 0.5; cursor: not-allowed; }
  .plus { font-size: 1.2em; line-height: 1; margin-top: -1px; }

  .list { list-style: none; padding: 0; margin: 0; display: flex; flex-direction: column; gap: 0.2rem; }
  .list li { display: grid; grid-template-columns: 1fr auto; align-items: stretch; }
  .row {
    display: grid;
    grid-template-columns: 0.7rem 4.5rem 1fr;
    align-items: center;
    gap: 0.55rem;
    padding: 0.45rem 0.6rem;
    font-family: var(--font-mono);
    font-size: 12px;
    text-align: left;
    color: var(--ink);
    border-left: 2px solid transparent;
    background: transparent;
    transition: background 120ms ease, border-color 120ms ease;
  }
  .row:hover { background: var(--cream-2); }
  .row.active {
    background: var(--cream-2);
    border-left-color: var(--coral);
    color: var(--coral-deep);
  }
  .dot { width: 8px; height: 8px; border-radius: 50%; background: var(--ink-faint); }
  .dot.on { background: var(--ok); }
  .cwd { color: var(--ink-3); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .kill {
    border: none;
    background: transparent;
    color: var(--ink-faint);
    padding: 0 0.5rem;
    font-size: 1.1rem;
    line-height: 1;
  }
  .kill:hover { color: var(--danger); }

  .empty { padding: 1rem; color: var(--ink-faint); font-size: 11px; text-align: center; letter-spacing: 0.04em; }
  .err { color: var(--danger); font-size: 12px; margin: 0; }

  .main {
    display: grid;
    grid-template-rows: auto 1fr;
    min-height: 0;
  }
  .top {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.75rem 1.25rem;
    border-bottom: 1px solid var(--line);
    background: var(--cream);
  }
  .hamburger { display: none; width: 24px; height: 24px; flex-direction: column; justify-content: center; gap: 4px; background: transparent; border: none; padding: 0; cursor: pointer; }
  .hamburger span { display: block; height: 1.5px; width: 100%; background: var(--ink-2); border-radius: 1px; }

  .crumbs { font-size: 12px; color: var(--ink); display: flex; align-items: center; gap: 0.5rem; flex-wrap: wrap; }
  .crumbs .id { color: var(--coral-dark); }
  .crumbs .sep { color: var(--ink-faint); }
  .ink-3 { color: var(--ink-3); }

  .empty-area { display: grid; place-items: center; padding: 3rem 1.25rem; }
  .empty-card {
    border: 1px dashed var(--line-strong);
    padding: 2rem 2rem 1.75rem;
    text-align: center;
    max-width: 28rem;
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }
  .empty-card h2 {
    font-size: 2rem;
    margin: 0;
    font-weight: 400;
    color: var(--ink);
    font-variation-settings: 'opsz' 144, 'SOFT' 50;
  }
  .ghost-link {
    color: var(--coral-dark);
    border-bottom: 1px solid currentColor;
    padding: 0;
    margin: 0 0.1em;
    background: transparent;
    cursor: pointer;
    font: inherit;
  }

  @media (max-width: 800px) {
    .layout { grid-template-columns: minmax(0, 1fr); }
    .side {
      position: absolute;
      top: 0; bottom: 0; left: 0;
      width: min(300px, 86vw);
      background: var(--cream);
      transform: translateX(-100%);
      transition: transform 220ms cubic-bezier(.2,.8,.2,1);
      z-index: 5;
      box-shadow: var(--shadow-2);
    }
    .layout.open .side { transform: translateX(0); }
    .hamburger { display: flex; }
  }
</style>
