<script lang="ts">
  import { onMount, onDestroy } from 'svelte'
  import { api } from '../lib/api'
  import {
    sessions,
    activeSessionId,
    frames,
    resetSessionState,
    logout,
    status,
  } from '../lib/stores'
  import { connectFrames, type FrameStream, type ConnectionState } from '../lib/ws'
  import type { Session } from '../lib/types'
  import Wordmark from '../components/Wordmark.svelte'
  import SessionList from '../components/SessionList.svelte'
  import NewSessionModal from '../components/NewSessionModal.svelte'
  import ClaudeLoginModal from '../components/ClaudeLoginModal.svelte'
  import TabBar from '../components/TabBar.svelte'
  import TerminalView from '../components/TerminalView.svelte'
  import ChatView from '../components/ChatView.svelte'
  import InspectorView from '../components/InspectorView.svelte'
  import InputBar from '../components/InputBar.svelte'
  import SideRail from '../components/SideRail.svelte'
  import StatusBadge from '../components/StatusBadge.svelte'
  import LangSwitch from '../components/LangSwitch.svelte'
  import { T } from '../lib/i18n'
  import type { ClaudeAuthStatus } from '../lib/types'

  let activeView = $state<'chat' | 'terminal' | 'inspector'>('chat')
  let stream: FrameStream | null = null
  let connState = $state<ConnectionState>('idle')
  let newOpen = $state(false)
  let sidebarOpen = $state(false)
  let railOpen = $state(false)
  let claudeAuth = $state<ClaudeAuthStatus | null>(null)
  let loginOpen = $state(false)

  async function refreshClaudeAuth() {
    try { claudeAuth = await api.claudeStatus() } catch { claudeAuth = null }
  }

  let activeId = $state('')
  activeSessionId.subscribe((v) => (activeId = v))

  let activeSession = $derived(
    $sessions.find((s: Session) => s.id === activeId) ?? null,
  )

  async function refreshSessions() {
    try {
      const r = await api.listSessions()
      sessions.set(r.sessions)
      // Auto-select most recent if nothing chosen.
      if (!activeId && r.sessions.length > 0) {
        const newest = [...r.sessions].sort((a, b) =>
          (b.created_at || '').localeCompare(a.created_at || ''),
        )[0]
        selectSession(newest.id)
      }
    } catch (err) {
      console.error('listSessions', err)
    }
  }

  function selectSession(id: string) {
    if (id === activeId) return
    if (stream) { stream.close(); stream = null }
    resetSessionState()
    activeSessionId.set(id)
    sidebarOpen = false
    if (!id) return
    // Replay history first so we are not staring at an empty screen.
    api.transcript(id).then((r) => {
      frames.set(r.frames || [])
      stream = connectFrames(id, {
        fromSeq: r.last_seq,
        onState: (s) => (connState = s),
      })
    }).catch((e) => {
      console.error('transcript', e)
      stream = connectFrames(id, { onState: (s) => (connState = s) })
    })
  }

  onMount(() => { refreshSessions(); refreshClaudeAuth() })
  onDestroy(() => stream?.close())

  // Refresh the session list whenever a stop frame arrives — quick proxy
  // for "list could be stale".
  frames.subscribe((fs) => {
    const last = fs[fs.length - 1]
    if (last && last.kind === 'stop') refreshSessions()
  })
</script>

<div class="layout" class:open-sidebar={sidebarOpen} class:open-rail={railOpen}>
  <aside class="sidebar">
    <div class="sb-head">
      <Wordmark size={26} />
      <div class="sb-head-right">
        <LangSwitch />
        <button class="ghost" onclick={() => logout()} title={$T('forget token', '清除 token')}>{$T('logout', '登出')}</button>
      </div>
    </div>

    <div class="sb-section">
      <span class="divider">{$T('sessions', '会话')}</span>
      <button class="new" onclick={() => (newOpen = true)}>
        <span class="plus">+</span><span>{$T('new session', '新建会话')}</span>
      </button>
    </div>

    <SessionList
      sessions={$sessions}
      activeId={activeId}
      onselect={selectSession}
    />

    <div class="sb-foot">
      <span class="divider">{$T('box', '盒子')}</span>
      <span class="meta">{$T('— a portable claude code dev env —', '— 便携 claude code 开发环境 —')}</span>
    </div>
  </aside>

  <main class="main">
    <header class="top">
      <button class="hamburger" onclick={() => (sidebarOpen = !sidebarOpen)} aria-label="toggle sessions">
        <span></span><span></span><span></span>
      </button>

      <div class="crumbs mono">
        {#if activeSession}
          <span class="ink-3">{$T('session', '会话')}</span>
          <span class="sep">/</span>
          <span class="id">{activeSession.id.slice(0, 8)}</span>
          {#if activeSession.model}
            <span class="sep">·</span>
            <span class="ink-3">{activeSession.model}</span>
          {/if}
        {:else}
          <span class="ink-3">{$T('no session', '没有会话')}</span>
        {/if}
      </div>

      <div class="top-right">
        {#if claudeAuth}
          {#if claudeAuth.loggedIn}
            <button
              class="auth-chip is-in mono"
              onclick={() => (loginOpen = true)}
              title={claudeAuth.email ?? ''}
            >
              <span class="dot"></span>
              <span>{claudeAuth.subscriptionType || 'claude.ai'}</span>
            </button>
          {:else}
            <button class="auth-chip mono" onclick={() => (loginOpen = true)}>
              <span class="dot pulse"></span>
              <span>{$T('sign in with claude', '用 Claude 登录')}</span>
            </button>
          {/if}
        {/if}
        {#if activeSession}
          <StatusBadge
            state={$status?.state ?? activeSession.state}
            connection={connState}
          />
        {/if}
        <button class="hamburger right" onclick={() => (railOpen = !railOpen)} aria-label="toggle rail">
          <span></span><span></span><span></span>
        </button>
      </div>
    </header>

    {#if activeSession}
      <TabBar
        active={activeView}
        oninput={(v) => (activeView = v)}
      />

      <section class="canvas">
        {#if activeView === 'chat'}
          <ChatView />
        {:else if activeView === 'terminal'}
          <TerminalView
            sessionId={activeSession.id}
          />
        {:else}
          <InspectorView />
        {/if}
      </section>

      <InputBar
        sessionId={activeSession.id}
        disabled={activeSession.state === 'stopped' || activeSession.state === 'failed'}
      />
    {:else}
      <section class="empty">
        <div class="empty-card">
          <span class="divider">{$T('empty', '空')}</span>
          <h2 class="serif">{$T('no session yet', '还没有会话')}</h2>
          <p class="ink-3">
            {$T('Start one and the panel will fill in.', '开一个,面板就会填满。')}
            <br />
            {$T('Press', '按')} <span class="kbd">N</span> {$T('or hit', '或点')}
            <button class="ghost-link" onclick={() => (newOpen = true)}>{$T('new session', '新建会话')}</button>.
          </p>
        </div>
      </section>
    {/if}
  </main>

  <aside class="rail">
    <SideRail />
  </aside>
</div>

{#if newOpen}
  <NewSessionModal
    onclose={() => (newOpen = false)}
    oncreated={(sess: Session) => {
      sessions.update((s) => [sess, ...s])
      selectSession(sess.id)
      newOpen = false
    }}
  />
{/if}

{#if loginOpen}
  <ClaudeLoginModal
    onclose={() => (loginOpen = false)}
    onsuccess={() => { loginOpen = false; refreshClaudeAuth() }}
  />
{/if}

<svelte:window onkeydown={(e) => {
  if ((e.key === 'n' || e.key === 'N') && !e.metaKey && !e.ctrlKey && document.activeElement?.tagName !== 'INPUT' && document.activeElement?.tagName !== 'TEXTAREA') {
    newOpen = true
    e.preventDefault()
  }
}} />

<style>
  .layout {
    display: grid;
    grid-template-columns: 280px minmax(0, 1fr) 320px;
    height: 100vh;
    color: var(--ink);
  }

  .sidebar, .rail {
    border-color: var(--line);
    background: var(--cream);
    overflow-y: auto;
    display: flex;
    flex-direction: column;
  }
  .sidebar {
    border-right: 1px solid var(--line);
    padding: 1rem 1rem 1.25rem;
    gap: 1.25rem;
  }
  .rail {
    border-left: 1px solid var(--line);
    padding: 1rem;
    background: linear-gradient(180deg, rgba(217,119,87,0.03), transparent 30%);
  }

  .sb-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
  }
  .sb-head-right {
    display: flex;
    align-items: center;
    gap: 0.4rem;
  }
  .sb-section { display: flex; flex-direction: column; gap: 0.65rem; }
  .sb-foot { margin-top: auto; display: flex; flex-direction: column; gap: 0.5rem; padding-top: 1rem; }
  .meta {
    font-family: var(--font-mono);
    font-size: 10.5px;
    text-align: center;
    color: var(--ink-faint);
    letter-spacing: 0.06em;
  }

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
  .new:hover { background: var(--coral); color: var(--cream); border-style: solid; }
  .plus { font-size: 1.2em; line-height: 1; margin-top: -1px; }

  .ghost {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--ink-3);
    padding: 0.25rem 0.5rem;
    border: 1px solid transparent;
    transition: color 120ms ease, border-color 120ms ease;
  }
  .ghost:hover {
    color: var(--coral-dark);
    border-color: var(--line-strong);
  }

  .ghost-link {
    color: var(--coral-dark);
    border-bottom: 1px solid currentColor;
    padding: 0;
    margin: 0 0.1em;
  }

  .main {
    display: grid;
    grid-template-rows: auto auto 1fr auto;
    min-height: 0;
  }

  .top {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    padding: 0.75rem 1.25rem;
    border-bottom: 1px solid var(--line);
    background: var(--cream);
  }

  .crumbs {
    font-size: 12px;
    color: var(--ink);
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
  }
  .crumbs .id { color: var(--coral-dark); }
  .crumbs .sep { color: var(--ink-faint); }
  .ink-3 { color: var(--ink-3); }

  .top-right { display: flex; align-items: center; gap: 0.6rem; flex-wrap: wrap; }

  .auth-chip {
    display: inline-flex;
    align-items: center;
    gap: 0.45rem;
    padding: 0.22rem 0.65rem;
    border: 1px dashed var(--coral);
    border-radius: 999px;
    font-size: 11px;
    color: var(--coral-dark);
    background: transparent;
    transition: background 120ms ease, color 120ms ease, border-style 120ms ease;
  }
  .auth-chip:hover { background: var(--coral); color: var(--cream); }
  .auth-chip .dot {
    width: 7px; height: 7px;
    border-radius: 50%;
    background: var(--coral);
  }
  .auth-chip.is-in {
    border-style: solid;
    color: var(--coral-deep);
  }
  .auth-chip.is-in .dot { background: var(--ok); }
  .auth-chip .dot.pulse { animation: pulse 1.4s ease-in-out infinite; }

  .hamburger {
    display: none;
    width: 24px;
    height: 24px;
    flex-direction: column;
    justify-content: center;
    gap: 4px;
  }
  .hamburger span {
    display: block;
    height: 1.5px;
    width: 100%;
    background: var(--ink-2);
    border-radius: 1px;
  }

  .canvas {
    overflow: hidden;
    min-height: 0;
    display: flex;
    flex-direction: column;
    background: var(--cream);
    background-image:
      linear-gradient(180deg, rgba(217,119,87,0.03), transparent 12rem);
  }

  .empty {
    display: grid;
    place-items: center;
    padding: 3rem 1.25rem;
  }
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
    letter-spacing: -0.01em;
    font-variation-settings: 'opsz' 144, 'SOFT' 50;
  }

  /* ---- responsive: tablet ---- */
  @media (max-width: 1200px) {
    .layout { grid-template-columns: 260px minmax(0, 1fr); }
    .rail {
      position: fixed;
      top: 0; bottom: 0; right: 0;
      width: min(320px, 88vw);
      transform: translateX(100%);
      transition: transform 220ms cubic-bezier(.2,.8,.2,1);
      box-shadow: var(--shadow-2);
      z-index: 20;
    }
    .layout.open-rail .rail { transform: translateX(0); }
    .hamburger.right { display: flex; }
  }

  /* ---- responsive: mobile ---- */
  @media (max-width: 800px) {
    .layout { grid-template-columns: minmax(0, 1fr); }
    .sidebar {
      position: fixed;
      top: 0; bottom: 0; left: 0;
      width: min(300px, 86vw);
      transform: translateX(-100%);
      transition: transform 220ms cubic-bezier(.2,.8,.2,1);
      box-shadow: var(--shadow-2);
      z-index: 20;
    }
    .layout.open-sidebar .sidebar { transform: translateX(0); }
    .hamburger { display: flex; }
  }
</style>
