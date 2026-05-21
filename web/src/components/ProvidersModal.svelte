<script lang="ts">
  import { onMount } from 'svelte'
  import { api, ApiError } from '../lib/api'
  import { T } from '../lib/i18n'
  import type { Provider, ProviderProbe } from '../lib/types'

  interface Props { onclose: () => void }
  let { onclose }: Props = $props()

  let providers = $state<Provider[]>([])
  let editingId = $state<string | null>(null)
  let form = $state({ label: '', api_host: 'https://api.anthropic.com', api_key: '', model: '' })
  let probe = $state<ProviderProbe | null>(null)
  let busy = $state(false)
  let probing = $state(false)
  let error = $state('')

  async function refresh() {
    try {
      const r = await api.listProviders()
      providers = r.providers
    } catch (e) {
      error = e instanceof ApiError ? e.message : (e as Error).message
    }
  }
  onMount(refresh)

  function resetForm() {
    editingId = null
    form = { label: '', api_host: 'https://api.anthropic.com', api_key: '', model: '' }
    probe = null
    error = ''
  }

  function editStart(p: Provider) {
    editingId = p.id
    form = {
      label: p.label,
      api_host: p.api_host,
      api_key: '', // never round-trip the redacted key
      model: p.model ?? '',
    }
    probe = null
    error = ''
  }

  async function save() {
    busy = true
    error = ''
    try {
      const payload = {
        label: form.label.trim(),
        api_host: form.api_host.trim(),
        api_key: form.api_key.trim(),
        model: form.model.trim() || undefined,
      }
      if (editingId) {
        await api.replaceProvider(editingId, payload)
      } else {
        await api.addProvider(payload)
      }
      await refresh()
      resetForm()
    } catch (e) {
      error = e instanceof ApiError ? e.message : (e as Error).message
    } finally {
      busy = false
    }
  }

  async function runProbe() {
    probing = true
    probe = null
    try {
      probe = await api.probeProvider({
        id: editingId && !form.api_key ? editingId : undefined,
        api_host: form.api_host,
        api_key: form.api_key || undefined,
        model: form.model || undefined,
      })
    } catch (e) {
      error = e instanceof ApiError ? e.message : (e as Error).message
    } finally {
      probing = false
    }
  }

  async function del(id: string) {
    if (!confirm($T('delete this provider?', '删除这个 provider?'))) return
    try {
      await api.deleteProvider(id)
      await refresh()
      if (editingId === id) resetForm()
    } catch (e) {
      error = e instanceof ApiError ? e.message : (e as Error).message
    }
  }
</script>

<div class="backdrop" onclick={onclose} role="presentation"></div>

<div class="modal" role="dialog" aria-modal="true">
  <header>
    <span class="divider">{$T('providers', '第三方 API')}</span>
    <button class="x" onclick={onclose} aria-label={$T('close', '关闭')}>×</button>
  </header>

  <h2 class="serif">{$T('Third-party Claude endpoints.', '第三方 Claude 兼容端点。')}</h2>
  <p class="lede serif">
    {$T(
      'Stored providers populate the api-key dropdown when starting a session. Replace overwrites the prior secret atomically — old keys never linger on disk.',
      '保存后的 provider 会出现在新建会话的 api-key 下拉里。每次修改是原子替换 —— 老 key 不会残留磁盘。'
    )}
  </p>

  <section class="list">
    {#if providers.length === 0}
      <div class="empty mono">{$T('— no providers yet —', '— 还没有 provider —')}</div>
    {/if}
    {#each providers as p (p.id)}
      <div class="row" class:active={editingId === p.id}>
        <div class="row-main">
          <span class="lbl">{p.label}</span>
          <span class="host mono">{p.api_host}</span>
          {#if p.model}<span class="model mono">{p.model}</span>{/if}
          <span class="key mono">{p.api_key || ''}</span>
        </div>
        <div class="row-actions">
          <button class="ghost" onclick={() => editStart(p)}>{$T('edit', '编辑')}</button>
          <button class="ghost danger" onclick={() => del(p.id)}>{$T('delete', '删除')}</button>
        </div>
      </div>
    {/each}
  </section>

  <section class="form">
    <span class="divider">{editingId ? $T('edit', '编辑') : $T('add new', '新增')}</span>

    <label class="field">
      <span class="lab">{$T('label', '名称')}</span>
      <input bind:value={form.label} placeholder="Anthropic / 8gpt / claude-proxy / …" disabled={busy} />
    </label>

    <label class="field">
      <span class="lab">api_host</span>
      <input bind:value={form.api_host} placeholder="https://api.anthropic.com" spellcheck="false" disabled={busy} />
    </label>

    <label class="field">
      <span class="lab">api_key</span>
      <input
        type="password"
        bind:value={form.api_key}
        placeholder={editingId ? $T('leave blank to keep existing', '留空保留原 key') : 'sk-…'}
        spellcheck="false"
        autocomplete="off"
        disabled={busy}
      />
    </label>

    <label class="field">
      <span class="lab">{$T('model · optional', '模型 · 可选')}</span>
      <input bind:value={form.model} placeholder="claude-opus-4-7 / claude-sonnet-4-6 / …" disabled={busy} />
    </label>

    {#if probe}
      <div class="probe mono" class:ok={probe.ok}>
        {probe.ok ? '[ ok ]' : '[ fail ]'}
        · {probe.http || 'net'} · {probe.latency_ms}ms · {probe.endpoint}
        {#if probe.detail}<br /><span class="dim">{probe.detail}</span>{/if}
      </div>
    {/if}

    {#if error}<p class="err mono">[ {error} ]</p>{/if}

    <div class="actions">
      <button class="ghost" onclick={runProbe} disabled={busy || probing || (!editingId && (!form.api_host || !form.api_key))}>
        {probing ? $T('probing…', '检测中…') : $T('probe', '在线检测')}
      </button>
      <button class="primary" onclick={save} disabled={busy || !form.label || !form.api_host || (!editingId && !form.api_key)}>
        {busy ? $T('saving…', '保存中…') : editingId ? $T('replace', '覆盖保存') : $T('save', '保存')}
      </button>
      {#if editingId}
        <button class="ghost" onclick={resetForm}>{$T('cancel', '取消')}</button>
      {/if}
    </div>
  </section>
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
    width: min(46rem, 94vw);
    max-height: min(48rem, 92vh);
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
  .x { font-size: 1.4rem; line-height: 1; color: var(--ink-3); padding: 0 0.25rem; background: transparent; border: none; cursor: pointer; }
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
    line-height: 1.55;
  }

  .list { display: flex; flex-direction: column; gap: 0.25rem; }
  .row {
    display: grid;
    grid-template-columns: 1fr auto;
    gap: 0.75rem;
    padding: 0.6rem 0.75rem;
    border: 1px solid var(--line);
    background: var(--cream-2);
    align-items: center;
  }
  .row.active { border-color: var(--coral); background: rgba(217,119,87,0.05); }
  .row-main { display: flex; flex-wrap: wrap; align-items: baseline; gap: 0.6rem; min-width: 0; }
  .lbl { color: var(--ink); font-family: var(--font-display); font-size: 0.95rem; }
  .host, .model, .key { color: var(--ink-3); font-size: 11px; }
  .key { color: var(--ink-faint); }
  .row-actions { display: flex; gap: 0.4rem; }

  .empty { padding: 0.85rem; color: var(--ink-faint); font-size: 11px; text-align: center; letter-spacing: 0.04em; border: 1px dashed var(--line); }

  .form {
    display: flex;
    flex-direction: column;
    gap: 0.65rem;
    border-top: 1px dotted var(--line);
    padding-top: 0.85rem;
  }
  .field { display: flex; flex-direction: column; gap: 0.3rem; }
  .lab { font-family: var(--font-mono); font-size: 11px; color: var(--ink-3); letter-spacing: 0.04em; }
  .field input {
    border: none;
    border-bottom: 1px solid var(--line-strong);
    background: transparent;
    padding: 0.45rem 0.1rem;
    font-family: var(--font-mono);
    font-size: 0.9rem;
    color: var(--ink);
  }
  .field input:focus { outline: none; border-bottom-color: var(--coral); }

  .actions { display: flex; align-items: center; gap: 0.6rem; flex-wrap: wrap; padding-top: 0.4rem; }
  .primary {
    font-family: var(--font-mono);
    color: var(--cream);
    background: var(--ink);
    padding: 0.45rem 0.95rem;
    border-radius: var(--r-xs);
    border: 1px solid var(--ink);
    font-size: 12px;
    cursor: pointer;
  }
  .primary:hover:not(:disabled) { background: var(--coral-dark); border-color: var(--coral-dark); }
  .primary:disabled { opacity: 0.4; cursor: not-allowed; }
  .ghost {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--ink-3);
    padding: 0.35rem 0.7rem;
    border: 1px solid var(--line-strong);
    border-radius: var(--r-xs);
    background: transparent;
    cursor: pointer;
  }
  .ghost:hover:not(:disabled) { color: var(--coral-dark); border-color: var(--coral); }
  .ghost.danger:hover { color: var(--danger); border-color: var(--danger); }
  .ghost:disabled { opacity: 0.4; cursor: not-allowed; }

  .probe {
    padding: 0.6rem 0.75rem;
    border: 1px solid var(--danger);
    color: var(--danger);
    background: rgba(168, 40, 28, 0.06);
    font-size: 11px;
    letter-spacing: 0.03em;
  }
  .probe.ok { border-color: var(--ok); color: var(--ok); background: rgba(83, 124, 76, 0.08); }
  .dim { opacity: 0.7; }

  .err { color: var(--danger); font-size: 12px; margin: 0; }
</style>
