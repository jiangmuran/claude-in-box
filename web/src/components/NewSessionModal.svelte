<script lang="ts">
  import { onMount } from 'svelte'
  import { api, ApiError } from '../lib/api'
  import { T } from '../lib/i18n'
  import type { Session, Provider } from '../lib/types'
  import ProvidersModal from './ProvidersModal.svelte'

  interface Props {
    onclose: () => void
    oncreated: (s: Session) => void
  }
  let { onclose, oncreated }: Props = $props()

  let workdir = $state('/workspace')
  let model = $state('')
  let authMode = $state<'subscription' | 'api_key'>('subscription')
  let providerId = $state('')
  let providers = $state<Provider[]>([])
  let providersOpen = $state(false)
  let busy = $state(false)
  let error = $state('')

  onMount(async () => {
    // Apply saved defaults so the box does not ask the same question twice.
    try {
      const p = await api.getPrefs()
      if (p.default_auth_mode === 'subscription' || p.default_auth_mode === 'api_key') {
        authMode = p.default_auth_mode
      }
      if (p.default_provider_id) providerId = p.default_provider_id
      if (p.default_model) model = p.default_model
    } catch { /* prefs are optional */ }
    await refreshProviders()
  })

  async function refreshProviders() {
    try {
      const r = await api.listProviders()
      providers = r.providers
      // If the saved default no longer exists, clear it.
      if (providerId && !providers.find((p) => p.id === providerId)) providerId = ''
    } catch { /* ignore */ }
  }

  async function create(e: SubmitEvent) {
    e.preventDefault()
    busy = true
    error = ''
    try {
      const s = await api.createSession({
        workdir,
        model: model || undefined,
        auth_mode: authMode,
        provider_id: authMode === 'api_key' && providerId ? providerId : undefined,
        bypass_permissions: true,
      })
      // Remember the user's choices so the next launch is one-click.
      try {
        await api.patchPrefs({
          default_auth_mode: authMode,
          default_provider_id: authMode === 'api_key' ? (providerId || '-') : '-',
          default_model: model || '-',
        })
      } catch { /* best-effort */ }
      oncreated(s)
    } catch (err) {
      if (err instanceof ApiError) error = err.message
      else error = (err as Error).message
    } finally {
      busy = false
    }
  }
</script>

<div class="backdrop" onclick={onclose} role="presentation"></div>

<div class="modal" role="dialog" aria-modal="true" aria-labelledby="ns-title">
  <header>
    <span class="divider">{$T('new session', '新建会话')}</span>
    <button class="x" onclick={onclose} aria-label={$T('close', '关闭')}>×</button>
  </header>

  <h2 id="ns-title" class="serif">{$T('Spin up Claude Code.', '把 Claude Code 拉起来。')}</h2>
  <p class="lede serif">
    {$T(
      'The container will spawn claude in a PTY and start streaming structured frames the moment it boots.',
      '容器会在 PTY 里 spawn 一个 claude,启动那一刻就开始往外推结构化事件帧。'
    )}
  </p>

  <form onsubmit={create}>
    <label class="field">
      <span class="label">{$T('workdir', '工作目录')}</span>
      <input bind:value={workdir} spellcheck="false" />
    </label>

    <label class="field">
      <span class="label">{$T('model · optional', '模型 · 可选')}</span>
      <input
        bind:value={model}
        placeholder="claude-sonnet-4-6 / claude-opus-4-7 / …"
        spellcheck="false"
      />
    </label>

    <div class="field">
      <span class="label">{$T('billing', '计费')}</span>
      <div class="seg">
        <button
          type="button"
          class="seg-btn"
          class:active={authMode === 'subscription'}
          onclick={() => (authMode = 'subscription')}
        >
          {$T('subscription', '订阅')}
        </button>
        <button
          type="button"
          class="seg-btn"
          class:active={authMode === 'api_key'}
          onclick={() => (authMode = 'api_key')}
        >
          {$T('api key', 'api key')}
        </button>
      </div>
      {#if authMode === 'subscription'}
        <p class="hint mono">
          {$T(
            '— uses the in-container claude login (top-right chip) or CLAUDE_CODE_OAUTH_TOKEN env —',
            '— 用容器内 claude login(右上角 chip)或 CLAUDE_CODE_OAUTH_TOKEN env —'
          )}
        </p>
      {:else}
        <div class="provider-row">
          <select bind:value={providerId} disabled={busy}>
            <option value="">{$T('— ANTHROPIC_API_KEY env —', '— 用 ANTHROPIC_API_KEY env —')}</option>
            {#each providers as p (p.id)}
              <option value={p.id}>{p.label} · {p.api_host}{p.model ? ' · ' + p.model : ''}</option>
            {/each}
          </select>
          <button type="button" class="ghost" onclick={() => (providersOpen = true)}>
            {$T('manage', '管理')}
          </button>
        </div>
      {/if}
    </div>

    <div class="actions">
      <button class="primary" type="submit" disabled={busy}>
        {busy ? $T('spawning…', '启动中…') : $T('launch', '启动')} <span class="kbd">↵</span>
      </button>
      {#if error}<span class="err mono">[ {error} ]</span>{/if}
    </div>
  </form>
</div>

{#if providersOpen}
  <ProvidersModal onclose={() => { providersOpen = false; refreshProviders() }} />
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgba(44, 32, 26, 0.42);
    backdrop-filter: blur(2px);
    z-index: 30;
    animation: fade 200ms ease both;
  }
  .modal {
    position: fixed;
    top: 50%;
    left: 50%;
    transform: translate(-50%, -50%);
    z-index: 31;
    width: min(34rem, 92vw);
    background: var(--cream);
    border: 1px solid var(--line-strong);
    box-shadow: var(--shadow-2);
    padding: 1.5rem 1.5rem 1.25rem;
    display: flex;
    flex-direction: column;
    gap: 1rem;
    animation: rise 280ms cubic-bezier(.2,.8,.2,1) both;
  }
  .modal::before {
    content: '';
    position: absolute;
    inset: 6px;
    border: 1px dashed var(--line);
    pointer-events: none;
  }

  header { display: flex; justify-content: space-between; align-items: center; }
  .x {
    font-size: 1.3rem;
    line-height: 1;
    color: var(--ink-3);
    padding: 0 0.25rem;
  }
  .x:hover { color: var(--coral-dark); }

  h2 {
    font-size: 2rem;
    font-weight: 400;
    margin: 0;
    color: var(--ink);
    font-variation-settings: 'opsz' 144, 'SOFT' 50;
  }
  .lede {
    margin: 0;
    color: var(--ink-2);
    font-variation-settings: 'opsz' 14, 'SOFT' 70;
    font-size: 0.95rem;
  }

  form {
    display: flex;
    flex-direction: column;
    gap: 1rem;
    margin-top: 0.5rem;
  }
  .field { display: flex; flex-direction: column; gap: 0.4rem; }
  .field input {
    border: none;
    border-bottom: 1px solid var(--line-strong);
    background: transparent;
    padding: 0.5rem 0.1rem;
    font-family: var(--font-mono);
    font-size: 0.95rem;
  }
  .field input:focus { outline: none; border-bottom-color: var(--coral); }

  .seg {
    display: inline-flex;
    border: 1px solid var(--line-strong);
    width: max-content;
    border-radius: var(--r-xs);
    overflow: hidden;
  }
  .seg-btn {
    padding: 0.45rem 0.9rem;
    font-family: var(--font-mono);
    font-size: 12px;
    letter-spacing: 0.04em;
    color: var(--ink-3);
    border-right: 1px solid var(--line-strong);
    background: transparent;
    transition: background 120ms ease, color 120ms ease;
  }
  .seg-btn:last-child { border-right: none; }
  .seg-btn.active { background: var(--ink); color: var(--cream); }
  .seg-btn:hover:not(.active) { background: var(--cream-2); }

  .provider-row {
    display: grid;
    grid-template-columns: 1fr auto;
    gap: 0.5rem;
    margin-top: 0.25rem;
  }
  .provider-row select {
    border: 1px solid var(--line-strong);
    background: var(--cream);
    padding: 0.4rem 0.5rem;
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--ink);
    border-radius: var(--r-xs);
    min-width: 0;
  }
  .provider-row .ghost {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--ink-3);
    padding: 0.35rem 0.7rem;
    border: 1px solid var(--line-strong);
    border-radius: var(--r-xs);
    background: transparent;
    cursor: pointer;
  }
  .provider-row .ghost:hover { color: var(--coral-dark); border-color: var(--coral); }

  .hint {
    font-size: 11px;
    color: var(--ink-faint);
    letter-spacing: 0.04em;
  }

  .actions { display: flex; align-items: center; gap: 1rem; padding-top: 0.5rem; }
  .primary {
    font-family: var(--font-mono);
    color: var(--cream);
    background: var(--ink);
    padding: 0.55rem 1rem;
    border-radius: var(--r-xs);
    border: 1px solid var(--ink);
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.9rem;
  }
  .primary:hover:not(:disabled) { background: var(--coral-dark); border-color: var(--coral-dark); }
  .primary:disabled { opacity: 0.4; cursor: not-allowed; }
  .primary .kbd {
    background: rgba(255,255,255,0.1);
    border-color: rgba(255,255,255,0.25);
    color: var(--cream);
  }
  .err { color: var(--danger); font-size: 12px; }
</style>
