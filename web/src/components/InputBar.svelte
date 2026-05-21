<script lang="ts">
  import { api, ApiError } from '../lib/api'
  import { T } from '../lib/i18n'

  interface Props { sessionId: string; disabled: boolean }
  let { sessionId, disabled }: Props = $props()

  let text = $state('')
  let busy = $state(false)
  let error = $state('')
  let ta = $state<HTMLTextAreaElement | null>(null)

  async function submit() {
    if (!text.trim()) return
    busy = true; error = ''
    const payload = text.endsWith('\n') ? text : text + '\n'
    try {
      await api.sendInput(sessionId, payload)
      text = ''
      resize()
    } catch (err) {
      error = err instanceof ApiError ? err.message : (err as Error).message
    } finally {
      busy = false
      ta?.focus()
    }
  }

  async function interrupt() {
    try { await api.interrupt(sessionId) } catch {}
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      submit()
    }
    if (e.key === 'c' && e.ctrlKey) {
      e.preventDefault()
      interrupt()
    }
  }

  function resize() {
    if (!ta) return
    ta.style.height = 'auto'
    ta.style.height = Math.min(ta.scrollHeight, 220) + 'px'
  }
</script>

<form class="bar" onsubmit={(e) => { e.preventDefault(); submit() }}>
  <span class="prompt mono">›</span>
  <textarea
    bind:this={ta}
    bind:value={text}
    onkeydown={onkeydown}
    oninput={resize}
    rows="1"
    placeholder={disabled
      ? $T('session stopped — open a new one', '会话已结束 — 开一个新的')
      : $T('message claude · enter to send · shift+enter for newline', '给 claude 发消息 · 回车发送 · shift+回车换行')}
    {disabled}
    spellcheck="false"
  ></textarea>
  <div class="ctl">
    {#if error}
      <span class="err mono" title={error}>[ {$T('err', '错误')} ]</span>
    {/if}
    <button type="button" class="ghost" onclick={interrupt} disabled={disabled} title={$T('ctrl+c interrupt', 'ctrl+c 中断')}>
      <span class="mono">^C</span>
    </button>
    <button type="submit" class="send" disabled={disabled || busy || !text.trim()}>
      <span class="mono">{busy ? '·' : '↵'}</span>
    </button>
  </div>
</form>

<style>
  .bar {
    display: grid;
    grid-template-columns: auto 1fr auto;
    align-items: end;
    gap: 0.55rem;
    padding: 0.7rem 1rem;
    border-top: 1px solid var(--line);
    background: var(--cream);
    background-image: linear-gradient(0deg, rgba(217,119,87,0.03), transparent 60%);
  }

  .prompt {
    color: var(--coral);
    font-size: 1.05rem;
    padding-bottom: 0.5rem;
    user-select: none;
  }

  textarea {
    resize: none;
    border: none;
    background: transparent;
    outline: none;
    font-family: var(--font-mono);
    font-size: 0.95rem;
    line-height: 1.5;
    color: var(--ink);
    padding: 0.4rem 0;
    max-height: 220px;
    overflow-y: auto;
  }
  textarea::placeholder {
    color: var(--ink-faint);
    font-style: italic;
  }
  textarea:disabled { color: var(--ink-faint); }

  .ctl { display: flex; align-items: center; gap: 0.4rem; padding-bottom: 0.25rem; }

  .ghost {
    color: var(--ink-3);
    font-size: 11px;
    padding: 0.35rem 0.5rem;
    border: 1px solid var(--line);
    border-radius: var(--r-xs);
    transition: color 120ms ease, border-color 120ms ease;
  }
  .ghost:hover:not(:disabled) { color: var(--coral-dark); border-color: var(--coral); }

  .send {
    background: var(--ink);
    color: var(--cream);
    width: 2.2rem;
    height: 2rem;
    border-radius: var(--r-xs);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    transition: background 120ms ease, transform 80ms ease;
  }
  .send:hover:not(:disabled) { background: var(--coral-dark); }
  .send:active:not(:disabled) { transform: translateY(1px); }
  .send:disabled { opacity: 0.3; cursor: not-allowed; }

  .err { color: var(--danger); font-size: 11px; }
</style>
