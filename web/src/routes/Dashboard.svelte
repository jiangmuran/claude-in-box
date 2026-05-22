<script lang="ts">
  import { onMount } from 'svelte'
  import { api } from '../lib/api'
  import { logout } from '../lib/stores'
  import { T } from '../lib/i18n'
  import Wordmark from '../components/Wordmark.svelte'
  import LangSwitch from '../components/LangSwitch.svelte'
  import SessionsView from '../components/SessionsView.svelte'
  import ShellsView from '../components/ShellsView.svelte'
  import FilesView from '../components/FilesView.svelte'
  import ClaudeLoginModal from '../components/ClaudeLoginModal.svelte'
  import type { ClaudeAuthStatus } from '../lib/types'

  type Tab = 'sessions' | 'shells' | 'files'
  let tab = $state<Tab>('sessions')

  let claudeAuth = $state<ClaudeAuthStatus | null>(null)
  let loginOpen = $state(false)

  async function refreshClaudeAuth() {
    try { claudeAuth = await api.claudeStatus() } catch { claudeAuth = null }
  }

  onMount(refreshClaudeAuth)
</script>

<div class="shell">
  <header class="header">
    <div class="brand">
      <Wordmark size={26} />
    </div>

    <nav class="tabs" aria-label={$T('top tabs', '主视图')}>
      <button class="tab" class:active={tab === 'sessions'} onclick={() => (tab = 'sessions')}>
        <span class="lbl">{$T('sessions', '会话')}</span>
        <span class="sub mono">claude code</span>
      </button>
      <button class="tab" class:active={tab === 'shells'} onclick={() => (tab = 'shells')}>
        <span class="lbl">{$T('shells', '终端')}</span>
        <span class="sub mono">bash vtty</span>
      </button>
      <button class="tab" class:active={tab === 'files'} onclick={() => (tab = 'files')}>
        <span class="lbl">{$T('files', '文件')}</span>
        <span class="sub mono">view / edit</span>
      </button>
    </nav>

    <div class="rightside">
      <a class="doc-link mono" href="https://github.com/jiangmuran/claude-in-box" target="_blank" rel="noreferrer" title="github repo">github</a>
      <a class="doc-link mono" href="https://github.com/jiangmuran/claude-in-box/blob/main/docs/API.md" target="_blank" rel="noreferrer" title="API reference">docs</a>
      {#if claudeAuth}
        {#if claudeAuth.loggedIn}
          <button class="auth-chip is-in mono" onclick={() => (loginOpen = true)} title={claudeAuth.email ?? ''}>
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
      <LangSwitch />
      <button class="ghost" onclick={() => logout()} title={$T('forget token', '清除 token')}>{$T('logout', '登出')}</button>
    </div>
  </header>

  <div class="body">
    {#if tab === 'sessions'}
      <SessionsView />
    {:else if tab === 'shells'}
      <ShellsView />
    {:else}
      <FilesView />
    {/if}
  </div>
</div>

{#if loginOpen}
  <ClaudeLoginModal
    onclose={() => (loginOpen = false)}
    onsuccess={() => { loginOpen = false; refreshClaudeAuth() }}
  />
{/if}

<style>
  .shell {
    display: grid;
    grid-template-rows: auto 1fr;
    height: 100vh;
    color: var(--ink);
  }
  .header {
    display: grid;
    grid-template-columns: auto 1fr auto;
    align-items: center;
    gap: 1.25rem;
    padding: 0.6rem 1rem;
    border-bottom: 1px solid var(--line);
    background: var(--cream);
  }
  .brand { display: flex; align-items: center; }

  .tabs {
    display: flex;
    align-items: stretch;
    gap: 0;
    justify-content: center;
    overflow-x: auto;
  }
  .tab {
    position: relative;
    padding: 0.4rem 0.95rem 0.35rem;
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 0.05rem;
    color: var(--ink-3);
    border: none;
    border-bottom: 2px solid transparent;
    background: transparent;
    cursor: pointer;
    transition: color 120ms ease, border-color 120ms ease;
    white-space: nowrap;
  }
  .tab:hover { color: var(--ink); }
  .tab.active {
    color: var(--coral-deep);
    border-bottom-color: var(--coral);
  }
  .tab .lbl {
    font-family: var(--font-mono);
    font-size: 13px;
    letter-spacing: 0.02em;
  }
  .tab .sub {
    font-size: 10px;
    color: var(--ink-faint);
    letter-spacing: 0.16em;
    text-transform: uppercase;
  }

  .rightside { display: flex; align-items: center; gap: 0.5rem; flex-wrap: wrap; justify-content: flex-end; }
  .doc-link {
    font-size: 11px;
    color: var(--ink-3);
    border-bottom: 1px dotted var(--line-strong);
    text-decoration: none;
    padding: 0.1rem 0.15rem;
    transition: color 120ms ease, border-color 120ms ease;
  }
  .doc-link:hover { color: var(--coral-dark); border-color: var(--coral); }
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
    cursor: pointer;
    transition: background 120ms ease, color 120ms ease, border-style 120ms ease;
  }
  .auth-chip:hover { background: var(--coral); color: var(--cream); }
  .auth-chip .dot {
    width: 7px; height: 7px;
    border-radius: 50%;
    background: var(--coral);
  }
  .auth-chip.is-in { border-style: solid; color: var(--coral-deep); }
  .auth-chip.is-in .dot { background: var(--ok); }
  .auth-chip .dot.pulse { animation: pulse 1.4s ease-in-out infinite; }

  .ghost {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--ink-3);
    padding: 0.25rem 0.5rem;
    border: 1px solid transparent;
    background: transparent;
    cursor: pointer;
    transition: color 120ms ease, border-color 120ms ease;
  }
  .ghost:hover {
    color: var(--coral-dark);
    border-color: var(--line-strong);
  }

  .body {
    min-height: 0;
    display: flex;
    flex-direction: column;
  }

  @media (max-width: 700px) {
    .header { grid-template-columns: auto 1fr; row-gap: 0.5rem; padding-bottom: 0; }
    .tabs { grid-column: 1 / -1; justify-content: flex-start; }
    .rightside { grid-column: 2 / 3; }
  }
</style>
