<script lang="ts">
  import { onMount, onDestroy } from 'svelte'
  import { api, ApiError } from '../lib/api'
  import { T } from '../lib/i18n'
  import type { ClaudeFlowSnapshot, Provider, ProviderProbe } from '../lib/types'

  interface Props {
    onclose: () => void
    onsuccess: () => void
  }
  let { onclose, onsuccess }: Props = $props()

  // Auth-mode tab: pick ONCE here. Subscription + api_key are mutually
  // exclusive at the server (configuring one wipes the other), so the
  // rest of the UI doesn't re-ask per session.
  let tab = $state<'subscription' | 'api_key'>('subscription')
  let activeMode = $state<'subscription' | 'api_key' | ''>('')

  // jmrai.net preset — a known third-party provider users can pick
  // without configuring api_host manually.
  const JMRAI_HOST = 'https://jmrai.net'
  let jmraiKey = $state('')
  let customLabel = $state('')
  let customHost  = $state('https://api.anthropic.com')
  let customKey   = $state('')
  let customModel = $state('')
  let probe = $state<ProviderProbe | null>(null)
  let providers = $state<Provider[]>([])
  let saving = $state(false)
  let probing = $state(false)

  let flow = $state<ClaudeFlowSnapshot | null>(null)
  let code = $state('')
  let starting = $state(false)
  let verifying = $state(false)
  let error = $state('')
  let copied = $state(false)

  async function refreshAuthState() {
    try {
      const [ps, p] = await Promise.all([api.listProviders(), api.getPrefs()])
      providers = ps.providers
      if (p.default_auth_mode === 'subscription' || p.default_auth_mode === 'api_key') {
        activeMode = p.default_auth_mode
        tab = p.default_auth_mode
      }
    } catch { /* best-effort */ }
  }

  onMount(() => {
    refreshAuthState()
  })

  // Only auto-start the OAuth flow when the subscription tab is the
  // active one and we haven't started already.
  $effect(() => {
    if (tab === 'subscription' && !flow && !starting && !verifying) {
      start()
    }
    if (tab === 'api_key' && flow && (flow.state === 'starting' || flow.state === 'awaiting_code')) {
      api.claudeCancel(flow.id).catch(() => {})
      flow = null
      code = ''
    }
  })

  onDestroy(() => {
    if (flow && (flow.state === 'starting' || flow.state === 'awaiting_code')) {
      api.claudeCancel(flow.id).catch(() => {})
    }
  })

  async function start() {
    flow = null
    error = ''
    code = ''
    starting = true
    try {
      flow = await api.claudeStart({})
    } catch (e) {
      error = e instanceof ApiError ? e.message : (e as Error).message
    } finally {
      starting = false
    }
  }

  async function restart() {
    if (flow && (flow.state === 'starting' || flow.state === 'awaiting_code')) {
      try { await api.claudeCancel(flow.id) } catch { /* ignore */ }
    }
    await start()
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault()
    if (!flow || !code.trim()) return
    verifying = true
    error = ''
    try {
      const next = await api.claudeCode(flow.id, code.trim())
      flow = next
      if (next.state === 'done') {
        setTimeout(onsuccess, 600)
      } else {
        error = next.error || `flow ended in ${next.state}`
      }
    } catch (e) {
      if (e instanceof ApiError) {
        const data = (e.data ?? {}) as { snapshot?: ClaudeFlowSnapshot; retryable?: boolean }
        if (data.snapshot) flow = data.snapshot
        error = e.message
        // Server marks recoverable errors (claude rejected the pasted
        // code but is still re-prompting). Clear the input so the user
        // doesn't accidentally resubmit the same bad code.
        if (data.retryable && flow && flow.state === 'awaiting_code') {
          code = ''
        }
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
    } catch { /* ignore */ }
  }

  async function savePresetJmrai() {
    if (!jmraiKey.trim()) return
    saving = true; error = ''
    try {
      await api.addProvider({
        label: 'jmrai.net',
        api_host: JMRAI_HOST,
        api_key: jmraiKey.trim(),
      })
      await refreshAuthState()
      onsuccess()
    } catch (e) {
      error = e instanceof ApiError ? e.message : (e as Error).message
    } finally {
      saving = false
    }
  }

  async function saveCustom() {
    if (!customHost || !customKey || !customLabel) return
    saving = true; error = ''
    try {
      await api.addProvider({
        label:    customLabel.trim(),
        api_host: customHost.trim(),
        api_key:  customKey.trim(),
        model:    customModel.trim() || undefined,
      })
      await refreshAuthState()
      onsuccess()
    } catch (e) {
      error = e instanceof ApiError ? e.message : (e as Error).message
    } finally {
      saving = false
    }
  }

  async function probeCustom() {
    probing = true; probe = null; error = ''
    try {
      probe = await api.probeProvider({
        api_host: customHost,
        api_key:  customKey,
        model:    customModel || undefined,
      })
    } catch (e) {
      error = e instanceof ApiError ? e.message : (e as Error).message
    } finally {
      probing = false
    }
  }

  async function probeJmrai() {
    probing = true; probe = null; error = ''
    try {
      probe = await api.probeProvider({
        api_host: JMRAI_HOST,
        api_key:  jmraiKey,
      })
    } catch (e) {
      error = e instanceof ApiError ? e.message : (e as Error).message
    } finally {
      probing = false
    }
  }

  async function removeProvider(id: string) {
    try {
      await api.deleteProvider(id)
      await refreshAuthState()
    } catch { /* best-effort */ }
  }

  let isTerminalFailure = $derived(
    flow !== null && (flow.state === 'failed' || flow.state === 'timed_out' || flow.state === 'cancelled')
  )
</script>

<div class="backdrop" onclick={onclose} role="presentation"></div>

<div class="modal" role="dialog" aria-modal="true" aria-labelledby="ca-title">
  <header>
    <span class="divider">{$T('sign in with claude', '用 Claude 登录')}</span>
    <button class="x" onclick={onclose} aria-label={$T('close', '关闭')}>×</button>
  </header>

  <h2 id="ca-title" class="serif">{$T('Connect your Claude account.', '连接你的 Claude 账号。')}</h2>
  <p class="lede serif">
    {$T(
      'Pick one and only one. Switching wipes the other — sessions afterwards use whatever is active here.',
      '二选一,切换时另一种会被清除。新会话默认用这里的活动鉴权。'
    )}
  </p>

  <nav class="auth-tabs" role="tablist">
    <button type="button" role="tab" class="auth-tab" class:active={tab === 'subscription'} onclick={() => (tab = 'subscription')}>
      <span class="lbl">{$T('subscription', '订阅')}</span>
      <span class="sub mono">claude.ai · oauth</span>
      {#if activeMode === 'subscription'}<span class="active-dot" title={$T('currently active', '当前活动')}></span>{/if}
    </button>
    <button type="button" role="tab" class="auth-tab" class:active={tab === 'api_key'} onclick={() => (tab = 'api_key')}>
      <span class="lbl">{$T('api key', 'api key')}</span>
      <span class="sub mono">jmrai.net · third-party</span>
      {#if activeMode === 'api_key'}<span class="active-dot"></span>{/if}
    </button>
  </nav>

  {#if tab === 'subscription'}

  {#if starting}
    <div class="state mono"><span class="spinner"></span>{$T('starting flow…', '正在启动流程…')}</div>
  {:else if !flow}
    <div class="state err mono">[ {$T('flow could not start', '流程未能启动')}{error ? ' — ' + error : ''} ]
      <button class="retry" type="button" onclick={restart}>{$T('try again', '重试')}</button>
    </div>
  {:else if flow.state === 'done'}
    <div class="state ok mono">[ {$T('signed in — closing in a moment', '已登录 — 马上关闭')} ]</div>
  {:else}
    <ol class="steps">
      <li>
        <span class="num">01</span>
        <div class="step-body">
          <span class="step-text serif">{$T('Open this URL in any browser and authorise:', '在浏览器里打开这个 URL 并授权:')}</span>
          {#if flow.auth_url}
            <div class="url-row">
              <a href={flow.auth_url} target="_blank" rel="noreferrer" class="url-link mono">
                {flow.auth_url.length > 78 ? flow.auth_url.slice(0, 78) + '…' : flow.auth_url}
              </a>
              <button type="button" class="copy" onclick={copy} title={$T('copy url', '复制 URL')}>
                <span class="mono">{copied ? $T('copied', '已复制') : $T('copy', '复制')}</span>
              </button>
            </div>
          {/if}
        </div>
      </li>
      <li>
        <span class="num">02</span>
        <div class="step-body">
          <span class="step-text serif">
            {$T(
              'Claude redirects you to platform.claude.com and shows a one-time code. Paste it here:',
              'Claude 会把你跳转到 platform.claude.com 显示一段一次性 code,粘贴到这里:'
            )}
          </span>
          <form class="code-form" onsubmit={submit}>
            <input
              type="text"
              bind:value={code}
              placeholder={$T('paste code here…', '把 code 粘到这里…')}
              spellcheck="false"
              autocomplete="off"
              disabled={verifying || flow.state !== 'awaiting_code'}
            />
            <button class="primary" type="submit" disabled={verifying || !code.trim() || flow.state !== 'awaiting_code'}>
              {verifying ? $T('verifying…', '验证中…') : $T('finish', '完成')} <span class="kbd">↵</span>
            </button>
          </form>
        </div>
      </li>
    </ol>
  {/if}

  {#if isTerminalFailure}
    <div class="recover mono">
      <span>[ {flow?.state} — {error || flow?.error || $T('flow did not complete', '流程未完成')} ]</span>
      <button class="retry" type="button" onclick={restart}>{$T('start over', '重新开始')}</button>
    </div>
  {:else if error && flow && flow.state !== 'done'}
    <p class="err mono">[ {error} ]</p>
  {/if}

  {:else}
    <!-- api_key tab — preset jmrai.net + custom provider, single-shot save. -->
    <section class="preset">
      <div class="preset-head">
        <span class="who">jmrai.net</span>
        <span class="hdr mono">{$T('built-in preset', '内置预设')}</span>
      </div>
      <p class="lede serif">
        {$T(
          'Anthropic-compatible third-party host. Paste your jmrai.net API key and save — your future sessions route through it.',
          'Anthropic 兼容的第三方 endpoint。粘贴你的 jmrai.net API key 保存即可,之后会话都走这里。'
        )}
      </p>
      <input
        type="password"
        bind:value={jmraiKey}
        placeholder="sk-…"
        spellcheck="false"
        autocomplete="off"
        class="key-in mono"
      />
      <div class="actions">
        <button type="button" class="ghost" onclick={probeJmrai} disabled={!jmraiKey || probing}>
          {probing ? $T('probing…', '检测中…') : $T('probe', '在线检测')}
        </button>
        <button type="button" class="primary" onclick={savePresetJmrai} disabled={!jmraiKey || saving}>
          {saving ? $T('saving…', '保存中…') : $T('use jmrai.net', '使用 jmrai.net')}
        </button>
      </div>
      {#if probe}
        <div class="probe mono" class:ok={probe.ok}>
          {probe.ok ? '[ ok ]' : '[ fail ]'} · {probe.http || 'net'} · {probe.latency_ms}ms
          {#if probe.detail}<br /><span class="dim">{probe.detail}</span>{/if}
        </div>
      {/if}
    </section>

    <details class="custom-block">
      <summary class="mono">{$T('or: custom api host', '或:自定义 api host')}</summary>
      <div class="custom-form">
        <input bind:value={customLabel} placeholder={$T('label', '名称')} spellcheck="false" class="mono" />
        <input bind:value={customHost}  placeholder="https://api.anthropic.com" spellcheck="false" class="mono" />
        <input type="password" bind:value={customKey} placeholder="sk-…" spellcheck="false" autocomplete="off" class="mono" />
        <input bind:value={customModel} placeholder={$T('model · optional', '模型 · 可选')} spellcheck="false" class="mono" />
        <div class="actions">
          <button type="button" class="ghost" onclick={probeCustom} disabled={!customHost || !customKey || probing}>
            {probing ? $T('probing…', '检测中…') : $T('probe', '在线检测')}
          </button>
          <button type="button" class="primary" onclick={saveCustom} disabled={!customLabel || !customHost || !customKey || saving}>
            {saving ? $T('saving…', '保存中…') : $T('save provider', '保存 provider')}
          </button>
        </div>
      </div>
    </details>

    {#if providers.length > 0}
      <section class="existing">
        <span class="divider">{$T('saved providers', '已保存的 provider')}</span>
        <ul class="prov-list">
          {#each providers as p (p.id)}
            <li>
              <span class="lbl">{p.label}</span>
              <span class="host mono">{p.api_host}</span>
              <span class="key mono">{p.api_key || ''}</span>
              <button type="button" class="ghost danger" onclick={() => removeProvider(p.id)}>{$T('delete', '删除')}</button>
            </li>
          {/each}
        </ul>
      </section>
    {/if}

    {#if error}
      <p class="err mono">[ {error} ]</p>
    {/if}
  {/if}

  <footer>
    <span class="hint mono">{$T(
      "— credentials live in the container's $CLAUDE_CONFIG_DIR —",
      '— 凭据存放在容器的 $CLAUDE_CONFIG_DIR —'
    )}</span>
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
    /* IMPORTANT: keep this transform as the ONLY transform on .modal;
       app.css's `rise` keyframe is opacity-only specifically so it does
       not clobber this centering. */
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

  /* --- auth tabs --- */
  .auth-tabs {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0.5rem;
  }
  .auth-tab {
    position: relative;
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 0.15rem;
    padding: 0.65rem 0.85rem;
    background: var(--cream-2);
    border: 1px solid var(--line-strong);
    border-radius: var(--r-xs);
    cursor: pointer;
    text-align: left;
    transition: border-color 120ms ease, background 120ms ease;
  }
  .auth-tab:hover { border-color: var(--coral); }
  .auth-tab.active {
    background: var(--cream);
    border-color: var(--coral);
    box-shadow: inset 0 -2px 0 var(--coral);
  }
  .auth-tab .lbl { font-family: var(--font-display); font-size: 0.95rem; color: var(--ink); }
  .auth-tab .sub { font-size: 10px; color: var(--ink-faint); letter-spacing: 0.12em; text-transform: uppercase; }
  .auth-tab .active-dot {
    position: absolute;
    top: 0.5rem;
    right: 0.55rem;
    width: 6px; height: 6px;
    border-radius: 50%;
    background: var(--ok);
  }

  /* --- preset jmrai card --- */
  .preset {
    display: flex;
    flex-direction: column;
    gap: 0.55rem;
    border: 1px solid var(--line-strong);
    padding: 0.85rem 1rem;
    background: var(--cream);
  }
  .preset-head { display: flex; align-items: baseline; justify-content: space-between; gap: 0.5rem; }
  .preset-head .who { font-family: var(--font-display); color: var(--coral-deep); font-size: 1rem; }
  .preset-head .hdr { font-size: 10px; letter-spacing: 0.1em; text-transform: uppercase; color: var(--ink-faint); }
  .key-in {
    width: 100%;
    border: 1px solid var(--line-strong);
    background: var(--cream-2);
    padding: 0.5rem 0.65rem;
    font-size: 0.9rem;
    color: var(--ink);
  }
  .key-in:focus { outline: none; border-color: var(--coral); }
  .actions { display: flex; gap: 0.5rem; align-items: center; flex-wrap: wrap; }
  .ghost {
    padding: 0.4rem 0.85rem;
    border: 1px solid var(--line-strong);
    border-radius: var(--r-xs);
    background: transparent;
    color: var(--ink-3);
    font-family: var(--font-mono);
    font-size: 11px;
    cursor: pointer;
  }
  .ghost:hover:not(:disabled) { color: var(--coral-dark); border-color: var(--coral); }
  .ghost.danger:hover { color: var(--danger); border-color: var(--danger); }
  .ghost:disabled { opacity: 0.4; cursor: not-allowed; }

  .probe {
    padding: 0.55rem 0.7rem;
    border: 1px solid var(--danger);
    color: var(--danger);
    background: rgba(168, 40, 28, 0.06);
    font-size: 11px;
  }
  .probe.ok {
    border-color: var(--ok);
    color: var(--ok);
    background: rgba(83, 124, 76, 0.08);
  }
  .dim { opacity: 0.7; }

  .custom-block { border: 1px dashed var(--line-strong); padding: 0.5rem 0.85rem; }
  .custom-block summary {
    list-style: none;
    cursor: pointer;
    color: var(--ink-3);
    font-size: 12px;
    padding: 0.25rem 0;
  }
  .custom-block summary::-webkit-details-marker { display: none; }
  .custom-block summary::before { content: '▸ '; opacity: 0.6; }
  .custom-block[open] summary::before { content: '▾ '; }
  .custom-form {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    margin-top: 0.5rem;
  }
  .custom-form input {
    border: none;
    border-bottom: 1px solid var(--line-strong);
    background: transparent;
    padding: 0.4rem 0.1rem;
    font-size: 0.88rem;
    color: var(--ink);
  }
  .custom-form input:focus { outline: none; border-bottom-color: var(--coral); }

  .existing { display: flex; flex-direction: column; gap: 0.4rem; }
  .prov-list { list-style: none; padding: 0; margin: 0; display: flex; flex-direction: column; gap: 0.3rem; }
  .prov-list li {
    display: grid;
    grid-template-columns: minmax(5rem, 8rem) 1fr auto auto;
    gap: 0.65rem;
    align-items: baseline;
    padding: 0.4rem 0.6rem;
    border: 1px solid var(--line);
    background: var(--cream-2);
    font-size: 12px;
  }
  .prov-list .lbl { color: var(--ink); font-family: var(--font-display); }
  .prov-list .host { color: var(--ink-3); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .prov-list .key { color: var(--ink-faint); font-size: 11px; }

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

  .recover {
    border: 1px dashed var(--danger);
    background: rgba(168, 40, 28, 0.06);
    color: var(--danger);
    padding: 0.7rem 0.85rem;
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 0.75rem;
    font-size: 12px;
  }
  .retry {
    border: 1px solid currentColor;
    border-radius: var(--r-xs);
    padding: 0.3rem 0.7rem;
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--coral-dark);
    background: transparent;
    transition: background 120ms ease, color 120ms ease;
  }
  .retry:hover { background: var(--coral); color: var(--cream); border-color: var(--coral); }

  footer { padding-top: 0.4rem; text-align: center; }
  .hint {
    color: var(--ink-faint);
    font-size: 10.5px;
    letter-spacing: 0.06em;
  }
</style>
