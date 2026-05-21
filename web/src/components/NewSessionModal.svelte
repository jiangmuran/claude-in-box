<script lang="ts">
  import { api, ApiError } from '../lib/api'
  import type { Session } from '../lib/types'

  interface Props {
    onclose: () => void
    oncreated: (s: Session) => void
  }
  let { onclose, oncreated }: Props = $props()

  let workdir = $state('/workspace')
  let model = $state('')
  let authMode = $state<'subscription' | 'api_key'>('subscription')
  let busy = $state(false)
  let error = $state('')

  async function create(e: SubmitEvent) {
    e.preventDefault()
    busy = true
    error = ''
    try {
      const s = await api.createSession({
        workdir,
        model: model || undefined,
        auth_mode: authMode,
        bypass_permissions: true,
      })
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
    <span class="divider">new session</span>
    <button class="x" onclick={onclose} aria-label="close">×</button>
  </header>

  <h2 id="ns-title" class="serif">Spin up Claude Code.</h2>
  <p class="lede serif">
    The container will <code>spawn claude</code> in a PTY and start streaming
    structured frames the moment it boots.
  </p>

  <form onsubmit={create}>
    <label class="field">
      <span class="label">workdir</span>
      <input bind:value={workdir} spellcheck="false" />
    </label>

    <label class="field">
      <span class="label">model · optional</span>
      <input
        bind:value={model}
        placeholder="claude-sonnet-4-6 / claude-opus-4-7 / …"
        spellcheck="false"
      />
    </label>

    <div class="field">
      <span class="label">billing</span>
      <div class="seg">
        <button
          type="button"
          class="seg-btn"
          class:active={authMode === 'subscription'}
          onclick={() => (authMode = 'subscription')}
        >
          subscription
        </button>
        <button
          type="button"
          class="seg-btn"
          class:active={authMode === 'api_key'}
          onclick={() => (authMode = 'api_key')}
        >
          api key
        </button>
      </div>
      <p class="hint mono">
        — uses the container env (CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_API_KEY) —
      </p>
    </div>

    <div class="actions">
      <button class="primary" type="submit" disabled={busy}>
        {busy ? 'spawning…' : 'launch'} <span class="kbd">↵</span>
      </button>
      {#if error}<span class="err mono">[ {error} ]</span>{/if}
    </div>
  </form>
</div>

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
  .lede code { font-family: var(--font-mono); font-size: 0.85em; color: var(--coral-dark); }

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
