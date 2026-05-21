<script lang="ts">
  import { authToken } from '../lib/stores'
  import { api, ApiError } from '../lib/api'
  import { T } from '../lib/i18n'
  import Wordmark from '../components/Wordmark.svelte'
  import LangSwitch from '../components/LangSwitch.svelte'

  let token = $state('')
  let busy = $state(false)
  let error = $state('')
  let health = $state<{ version?: string; mode?: string } | null>(null)

  api.health().then((h) => (health = h)).catch(() => {})

  async function submit(e: SubmitEvent) {
    e.preventDefault()
    if (!token.trim()) return
    busy = true
    error = ''
    authToken.set(token.trim())
    try {
      await api.listSessions()
    } catch (err) {
      authToken.set('')
      if (err instanceof ApiError) {
        error = err.status === 401 || err.status === 403 ? $T('token rejected', 'token 不被接受') : err.message
      } else {
        error = (err as Error).message
      }
    } finally {
      busy = false
    }
  }
</script>

<main class="shell">
  <div class="grain"></div>

  <header class="top">
    <Wordmark size={28} />
    <span class="right">
      {#if health?.version}<span class="version">{health.version} · {health.mode}</span>{/if}
      <LangSwitch />
    </span>
  </header>

  <section class="hero">
    <div class="rule"><span class="divider">{$T('welcome', '欢迎')}</span></div>

    <h1 class="title serif">
      {$T('Run Claude Code', '让 Claude Code')} <br /><em class="em">{$T('anywhere.', '跑在任何地方。')}</em>
    </h1>

    <p class="lede serif">
      {$T(
        'You are looking at a control plane for a real, batteries-included development environment with Claude Code running inside it. To start, paste the master API key your box was minted with at boot',
        '这是一个把按需开发环境 + Claude Code 打包进去的控制面。开始之前,粘贴容器启动时生成的主 API key'
      )}
      (<code>CIB_AUTH_TOKEN</code>).
    </p>

    <form class="card" onsubmit={submit}>
      <label class="field">
        <span class="label">{$T('api key', 'api key')}</span>
        <input
          bind:value={token}
          placeholder={$T('paste your master token here', '粘贴主 token 到这里')}
          autocomplete="off"
          spellcheck="false"
          disabled={busy}
        />
      </label>
      <div class="actions">
        <button class="primary" type="submit" disabled={busy || !token.trim()}>
          {busy ? $T('verifying…', '验证中…') : $T('unlock', '解锁')} <span class="kbd">↵</span>
        </button>
        {#if error}
          <span class="err mono">[ {error} ]</span>
        {/if}
      </div>
    </form>

    <div class="rule"><span class="divider">{$T('what next', '接下来')}</span></div>

    <ol class="steps">
      <li>
        <span class="num">01</span>
        <span class="step-body">
          {$T('Validate this control plane is healthy:', '先确认这个控制面是健康的:')}
          <code>curl http://&lt;your-box&gt;:8080/api/health</code>.
        </span>
      </li>
      <li>
        <span class="num">02</span>
        <span class="step-body">
          {$T(
            'Pass CLAUDE_CODE_OAUTH_TOKEN (from `claude setup-token` on your laptop) or ANTHROPIC_API_KEY via docker run -e. The first session you start will inherit it.',
            '通过 docker run -e 注入 CLAUDE_CODE_OAUTH_TOKEN(在本机 `claude setup-token` 拿到)或 ANTHROPIC_API_KEY。新开的会话会继承这个凭据。'
          )}
        </span>
      </li>
      <li>
        <span class="num">03</span>
        <span class="step-body">
          {$T(
            'Want to talk to the box from a phone, MCU, or another agent? Mint a device-scoped token from the dashboard once you are in.',
            '想从手机、单片机或别的 agent 连这个 box?登录之后在仪表盘签发一个设备级 token 就行。'
          )}
        </span>
      </li>
    </ol>
  </section>

  <footer class="foot">
    <span class="mark">— claude-in-box · <a href="https://github.com/jiangmuran/claude-in-box" target="_blank" rel="noreferrer">github</a> —</span>
  </footer>
</main>

<style>
  .shell {
    min-height: 100vh;
    position: relative;
    padding: clamp(1.25rem, 3vw, 2.5rem);
    display: grid;
    grid-template-rows: auto 1fr auto;
    gap: 2rem;
    max-width: 56rem;
    margin: 0 auto;
  }

  .grain {
    position: fixed;
    inset: 0;
    pointer-events: none;
    opacity: 0.45;
    z-index: 0;
    background-image:
      radial-gradient(ellipse at 20% 0%, rgba(217, 119, 87, 0.10), transparent 55%),
      radial-gradient(ellipse at 100% 100%, rgba(255, 183, 107, 0.08), transparent 55%);
  }

  .top {
    display: flex;
    align-items: center;
    justify-content: space-between;
    z-index: 1;
    animation: fade 600ms ease 80ms both;
  }
  .right {
    display: flex;
    align-items: center;
    gap: 0.6rem;
  }
  .version {
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--ink-faint);
  }

  .hero {
    position: relative;
    z-index: 1;
    display: flex;
    flex-direction: column;
    gap: 1.5rem;
    align-self: center;
    animation: rise 700ms cubic-bezier(.2,.8,.2,1) 120ms both;
  }

  .rule { width: 100%; }

  .title {
    font-size: clamp(2.5rem, 6vw, 4.5rem);
    font-weight: 400;
    line-height: 1;
    letter-spacing: -0.02em;
    color: var(--ink);
    font-variation-settings: 'opsz' 144, 'SOFT' 50, 'WONK' 0;
    margin: 0;
  }
  .em {
    color: var(--coral-dark);
    font-style: italic;
    font-variation-settings: 'opsz' 144, 'SOFT' 100, 'WONK' 1;
  }

  .lede {
    font-size: clamp(1rem, 1.6vw, 1.15rem);
    line-height: 1.6;
    max-width: 38rem;
    color: var(--ink-2);
    font-variation-settings: 'opsz' 14, 'SOFT' 80;
    margin: 0;
  }
  .lede code {
    font-family: var(--font-mono);
    font-size: 0.85em;
    color: var(--coral-dark);
  }

  .card {
    border: 1px solid var(--line-strong);
    background: var(--cream);
    padding: 1.25rem 1.25rem 1rem;
    display: flex;
    flex-direction: column;
    gap: 1rem;
    box-shadow: var(--shadow-1);
    position: relative;
  }
  .card::before {
    content: '';
    position: absolute;
    top: 6px;
    left: 6px;
    right: 6px;
    bottom: 6px;
    border: 1px dashed var(--line);
    pointer-events: none;
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    position: relative;
  }
  .field input {
    background: transparent;
    border: none;
    border-bottom: 1px solid var(--line-strong);
    padding: 0.6rem 0.1rem;
    font-family: var(--font-mono);
    font-size: 1rem;
    color: var(--ink);
    transition: border-color 160ms ease;
  }
  .field input:focus {
    outline: none;
    border-bottom-color: var(--coral);
  }
  .field input::placeholder { color: var(--ink-faint); }

  .actions {
    display: flex;
    align-items: center;
    gap: 1rem;
    flex-wrap: wrap;
  }
  .primary {
    font-family: var(--font-mono);
    font-size: 0.92rem;
    letter-spacing: 0.04em;
    color: var(--cream);
    background: var(--ink);
    padding: 0.6rem 1rem;
    border: 1px solid var(--ink);
    border-radius: var(--r-xs);
    display: inline-flex;
    align-items: center;
    gap: 0.6rem;
    transition: background 160ms ease, transform 80ms ease;
  }
  .primary:hover:not(:disabled) {
    background: var(--coral-dark);
    border-color: var(--coral-dark);
  }
  .primary:active:not(:disabled) { transform: translateY(1px); }
  .primary:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }
  .primary .kbd {
    background: rgba(255,255,255,0.1);
    border-color: rgba(255,255,255,0.25);
    color: var(--cream);
  }

  .err {
    color: var(--danger);
    font-size: 12px;
  }

  .steps {
    list-style: none;
    padding: 0;
    margin: 0;
    display: grid;
    grid-template-columns: 1fr;
    gap: 0.75rem;
  }
  .steps li {
    display: grid;
    grid-template-columns: 3.25rem 1fr;
    gap: 0.5rem;
    align-items: baseline;
    padding: 0.6rem 0;
    border-top: 1px dotted var(--line);
  }
  .steps li:first-child { border-top: none; }
  .num {
    font-family: var(--font-mono);
    color: var(--coral-dark);
    font-size: 0.92rem;
    letter-spacing: 0.05em;
  }
  .step-body {
    font-family: var(--font-display);
    font-variation-settings: 'opsz' 14, 'SOFT' 70;
    font-size: 0.98rem;
    line-height: 1.55;
    color: var(--ink-2);
  }
  .step-body code {
    font-family: var(--font-mono);
    font-size: 0.82em;
    color: var(--coral-dark);
    background: var(--cream-2);
    padding: 0.05em 0.4em;
    border-radius: var(--r-xs);
  }

  .foot {
    text-align: center;
    color: var(--ink-faint);
    font-family: var(--font-mono);
    font-size: 11px;
    letter-spacing: 0.18em;
    text-transform: uppercase;
    z-index: 1;
  }
  .mark a { color: var(--coral-dark); }

  @media (max-width: 540px) {
    .shell { padding: 1rem; gap: 1.5rem; }
  }
</style>
