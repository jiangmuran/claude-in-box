<script lang="ts">
  import { onMount, onDestroy } from 'svelte'
  import { api, ApiError } from '../lib/api'
  import type { ClaudeFlowSnapshot } from '../lib/types'

  interface Props {
    onclose: () => void
    onsuccess: () => void
  }
  let { onclose, onsuccess }: Props = $props()

  let flow = $state<ClaudeFlowSnapshot | null>(null)
  let code = $state('')
  let starting = $state(true)
  let verifying = $state(false)
  let error = $state('')
  let copied = $state(false)

  onMount(async () => {
    try {
      flow = await api.claudeStart({})
    } catch (e) {
      error = e instanceof ApiError ? e.message : (e as Error).message
    } finally {
      starting = false
    }
  })

  onDestroy(() => {
    // If we navigate away without finishing, cancel the in-flight flow on
    // the server so it does not linger.
    if (flow && (flow.state === 'starting' || flow.state === 'awaiting_code')) {
      api.claudeCancel(flow.id).catch(() => {})
    }
  })

  async function submit(e: SubmitEvent) {
    e.preventDefault()
    if (!flow || !code.trim()) return
    verifying = true
    error = ''
    try {
      const next = await api.claudeCode(flow.id, code.trim())
      flow = next
      if (next.state === 'done') {
        // Brief pause to let the success state visibly land before closing.
        setTimeout(onsuccess, 600)
      } else {
        error = next.error || `flow ended in ${next.state}`
      }
    } catch (e) {
      if (e instanceof ApiError) {
        const snap = (e.data as { snapshot?: ClaudeFlowSnapshot } | undefined)?.snapshot
        if (snap) flow = snap
        error = e.message
      } else {
        error = (e as Error).message
      }
    } finally {
      verifying = false
    }
  }

  async function copy() {
    if (!flow?.auth_url) return
    try {
      await navigator.clipboard.writeText(flow.auth_url)
      copied = true
      setTimeout(() => (copied = false), 1400)
    } catch {
      // ignore — manual select-copy will still work
    }
  }
</script>

<div class="backdrop" onclick={onclose} role="presentation"></div>

<div class="modal" role="dialog" aria-modal="true" aria-labelledby="ca-title">
  <header>
    <span class="divider">sign in with claude</span>
    <button class="x" onclick={onclose} aria-label="close">×</button>
  </header>

  <h2 id="ca-title" class="serif">Connect your Claude account.</h2>
  <p class="lede serif">
    The container will keep its own Claude credentials. Once signed in,
    new sessions billing as <em class="em">subscription</em> use your
    interactive Pro/Max quota — the only path that stays on the interactive
    quota after the <a href="https://www.anthropic.com/news/agent-sdk-quotas" target="_blank" rel="noreferrer">2026-06-15 Agent SDK quota split</a>.
  </p>

  {#if starting}
    <div class="state mono"><span class="spinner"></span>starting flow…</div>
  {:else if !flow}
    <div class="state err mono">[ flow could not start{error ? ' — ' + error : ''} ]</div>
  {:else if flow.state === 'done'}
    <div class="state ok mono">[ signed in — closing in a moment ]</div>
  {:else}
    <ol class="steps">
      <li>
        <span class="num">01</span>
        <div class="step-body">
          <span class="step-text serif">Open this URL in any browser and authorise:</span>
          {#if flow.auth_url}
            <div class="url-row">
              <a href={flow.auth_url} target="_blank" rel="noreferrer" class="url-link mono">
                {flow.auth_url.length > 78 ? flow.auth_url.slice(0, 78) + '…' : flow.auth_url}
              </a>
              <button type="button" class="copy" onclick={copy} title="copy url">
                <span class="mono">{copied ? 'copied' : 'copy'}</span>
              </button>
            </div>
          {/if}
        </div>
      </li>
      <li>
        <span class="num">02</span>
        <div class="step-body">
          <span class="step-text serif">Claude redirects you to <code>platform.claude.com</code> and shows a one-time code. Paste it here:</span>
          <form class="code-form" onsubmit={submit}>
            <input
              type="text"
              bind:value={code}
              placeholder="paste code here…"
              spellcheck="false"
              autocomplete="off"
              disabled={verifying || flow.state !== 'awaiting_code'}
            />
            <button class="primary" type="submit" disabled={verifying || !code.trim() || flow.state !== 'awaiting_code'}>
              {verifying ? 'verifying…' : 'finish'} <span class="kbd">↵</span>
            </button>
          </form>
        </div>
      </li>
    </ol>
  {/if}

  {#if error && flow && flow.state !== 'done'}
    <p class="err mono">[ {error} ]</p>
  {/if}

  <footer>
    <span class="hint mono">— credentials live in the container's $CLAUDE_CONFIG_DIR —</span>
  </footer>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgba(44, 32, 26, 0.5);
    backdrop-filter: blur(3px);
    z-index: 30;
    animation: fade 200ms ease both;
  }
  .modal {
    position: fixed;
    top: 50%;
    left: 50%;
    transform: translate(-50%, -50%);
    z-index: 31;
    width: min(42rem, 94vw);
    max-height: min(46rem, 92vh);
    overflow-y: auto;
    background: var(--cream);
    border: 1px solid var(--line-strong);
    box-shadow: var(--shadow-2);
    padding: 1.5rem 1.75rem 1.25rem;
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
    font-size: 1.4rem;
    line-height: 1;
    color: var(--ink-3);
    padding: 0 0.25rem;
  }
  .x:hover { color: var(--coral-dark); }

  h2 {
    font-size: 2.1rem;
    font-weight: 400;
    margin: 0;
    color: var(--ink);
    font-variation-settings: 'opsz' 144, 'SOFT' 50;
    letter-spacing: -0.01em;
  }
  .em {
    color: var(--coral-dark);
    font-style: italic;
    font-variation-settings: 'opsz' 144, 'SOFT' 100, 'WONK' 1;
  }
  .lede {
    margin: 0;
    color: var(--ink-2);
    font-variation-settings: 'opsz' 14, 'SOFT' 70;
    font-size: 0.95rem;
    line-height: 1.55;
  }
  .lede a { color: var(--coral-dark); }

  .state {
    padding: 0.85rem 1rem;
    border: 1px solid var(--line-strong);
    background: var(--cream-2);
    color: var(--ink-2);
    font-size: 12px;
    letter-spacing: 0.04em;
    display: flex;
    align-items: center;
    gap: 0.65rem;
  }
  .state.ok { color: var(--coral-deep); border-color: var(--coral); background: rgba(217,119,87,0.08); }
  .state.err { color: var(--danger); border-color: var(--danger); }
  .spinner {
    width: 10px; height: 10px;
    border-radius: 50%;
    background: var(--coral);
    animation: pulse 1s ease-in-out infinite;
  }

  .steps {
    list-style: none;
    padding: 0;
    margin: 0.25rem 0 0;
    display: grid;
    gap: 1rem;
  }
  .steps li {
    display: grid;
    grid-template-columns: 3.25rem 1fr;
    gap: 0.5rem;
    padding: 0.6rem 0 0.4rem;
    border-top: 1px dotted var(--line);
  }
  .steps li:first-child { border-top: none; padding-top: 0; }
  .num {
    font-family: var(--font-mono);
    color: var(--coral-dark);
    letter-spacing: 0.05em;
  }
  .step-body {
    display: flex;
    flex-direction: column;
    gap: 0.6rem;
  }
  .step-text {
    font-family: var(--font-display);
    font-variation-settings: 'opsz' 14, 'SOFT' 70;
    color: var(--ink-2);
  }
  .step-text code {
    font-family: var(--font-mono);
    font-size: 0.85em;
    color: var(--coral-dark);
  }

  .url-row {
    display: flex;
    align-items: stretch;
    border: 1px solid var(--line-strong);
    background: var(--cream-2);
  }
  .url-link {
    flex: 1;
    padding: 0.55rem 0.7rem;
    color: var(--ink);
    font-size: 12px;
    border-bottom: none;
    word-break: break-all;
    text-decoration: none;
  }
  .url-link:hover { color: var(--coral-dark); background: rgba(217,119,87,0.06); }
  .copy {
    padding: 0 0.85rem;
    border-left: 1px solid var(--line-strong);
    color: var(--ink-3);
    font-size: 11px;
    letter-spacing: 0.04em;
    background: var(--cream);
  }
  .copy:hover { color: var(--coral-dark); background: var(--cream-2); }

  .code-form {
    display: grid;
    grid-template-columns: 1fr auto;
    gap: 0.5rem;
    align-items: stretch;
  }
  .code-form input {
    background: transparent;
    border: 1px solid var(--line-strong);
    padding: 0.55rem 0.7rem;
    font-family: var(--font-mono);
    font-size: 0.9rem;
    color: var(--ink);
  }
  .code-form input:focus { outline: none; border-color: var(--coral); }

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
  .primary .kbd { background: rgba(255,255,255,0.1); border-color: rgba(255,255,255,0.25); color: var(--cream); }

  .err {
    color: var(--danger);
    font-size: 12px;
    margin: 0;
  }

  footer { padding-top: 0.4rem; text-align: center; }
  .hint {
    color: var(--ink-faint);
    font-size: 10.5px;
    letter-spacing: 0.06em;
  }
</style>
