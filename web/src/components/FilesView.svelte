<script lang="ts">
  import { onMount } from 'svelte'
  import { api, ApiError } from '../lib/api'
  import { T } from '../lib/i18n'
  import type { FSEntry } from '../lib/types'

  let roots = $state<string[]>([])
  let root = $state('workspace')
  let path = $state('')
  let entries = $state<FSEntry[]>([])
  let selectedPath = $state('')
  let selectedIsDir = $state(false)
  let content = $state('')
  let originalContent = $state('')
  let truncated = $state(false)
  let dirty = $derived(content !== originalContent)
  let busy = $state(false)
  let saving = $state(false)
  let error = $state('')

  onMount(async () => {
    try {
      const r = await api.fsRoots()
      roots = r.roots
      if (!roots.includes(root)) root = roots[0] || 'workspace'
    } catch { /* ignore */ }
    await refreshList()
  })

  async function refreshList() {
    busy = true; error = ''
    try {
      const r = await api.fsList(root, path)
      entries = r.entries
    } catch (e) {
      error = e instanceof ApiError ? e.message : (e as Error).message
      entries = []
    } finally {
      busy = false
    }
  }

  function up() {
    if (!path) return
    const parts = path.split('/').filter(Boolean)
    parts.pop()
    path = parts.join('/')
    selectedPath = ''
    content = originalContent = ''
    refreshList()
  }

  async function open(e: FSEntry) {
    if (e.is_dir) {
      path = e.path
      selectedPath = ''
      content = originalContent = ''
      await refreshList()
      return
    }
    selectedPath = e.path
    selectedIsDir = false
    busy = true; error = ''
    try {
      const r = await api.fsRead(root, e.path)
      content = r.content
      originalContent = r.content
      truncated = r.truncated
    } catch (err) {
      error = err instanceof ApiError ? err.message : (err as Error).message
    } finally {
      busy = false
    }
  }

  async function save() {
    if (!selectedPath) return
    saving = true; error = ''
    try {
      await api.fsWrite(root, selectedPath, content)
      originalContent = content
    } catch (err) {
      error = err instanceof ApiError ? err.message : (err as Error).message
    } finally {
      saving = false
    }
  }

  function formatSize(n: number) {
    if (n < 1024) return `${n} B`
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`
    return `${(n / 1024 / 1024).toFixed(1)} MiB`
  }
</script>

<div class="layout">
  <aside class="side">
    <div class="head">
      <span class="divider">{$T('files', '文件')}</span>
      <div class="roots">
        {#each roots as r (r)}
          <button
            class="root-chip mono"
            class:active={r === root}
            onclick={() => { root = r; path = ''; selectedPath = ''; content = originalContent = ''; refreshList() }}
          >{r}</button>
        {/each}
      </div>
      <div class="path mono">
        <button class="up" onclick={up} disabled={!path} title={$T('up', '上一级')}>↑</button>
        <span class="cur">/{path}</span>
      </div>
    </div>

    {#if error}<p class="err mono">[ {error} ]</p>{/if}

    <ul class="list">
      {#each entries as e (e.path)}
        <li>
          <button
            class="row"
            class:active={e.path === selectedPath}
            onclick={() => open(e)}
          >
            <span class="ic">{e.is_dir ? '▸' : '·'}</span>
            <span class="name">{e.name}</span>
            <span class="meta">{e.is_dir ? '' : formatSize(e.size)}</span>
          </button>
        </li>
      {/each}
      {#if entries.length === 0 && !busy}
        <li class="empty mono">{$T('— empty —', '— 空 —')}</li>
      {/if}
    </ul>
  </aside>

  <main class="main">
    <header class="top">
      <div class="crumbs mono">
        <span class="ink-3">{root}</span>
        <span class="sep">/</span>
        <span class="id">{selectedPath || $T('select a file', '选一个文件')}</span>
        {#if truncated}<span class="warn mono">[ {$T('truncated', '已截断')} ]</span>{/if}
      </div>
      <div class="actions">
        {#if selectedPath && !selectedIsDir}
          <button class="primary" onclick={save} disabled={!dirty || saving}>
            {saving ? $T('saving…', '保存中…') : dirty ? $T('save', '保存') : $T('saved', '已保存')}
            <span class="kbd">⌘S</span>
          </button>
        {/if}
      </div>
    </header>

    {#if selectedPath && !selectedIsDir}
      <textarea
        class="editor mono"
        bind:value={content}
        spellcheck="false"
        onkeydown={(e) => {
          if ((e.metaKey || e.ctrlKey) && e.key === 's') {
            e.preventDefault()
            save()
          }
        }}
      ></textarea>
    {:else}
      <section class="empty-area">
        <div class="empty-card">
          <span class="divider">{$T('preview', '预览')}</span>
          <h2 class="serif">{$T('Pick a file.', '选一个文件。')}</h2>
          <p class="ink-3">
            {$T(
              'README, ARCHITECTURE, hooks.json, anything in /workspace or /home/coder/.claude — view or edit in place. Saves write through the box.',
              '在 /workspace 或 /home/coder/.claude 里挑一个 — README、架构、hooks.json,看完直接改保存,容器内同步生效。'
            )}
          </p>
        </div>
      </section>
    {/if}
  </main>
</div>

<style>
  .layout {
    display: grid;
    grid-template-columns: 300px minmax(0, 1fr);
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
  .head { display: flex; flex-direction: column; gap: 0.6rem; }
  .roots { display: flex; gap: 0.3rem; flex-wrap: wrap; }
  .root-chip {
    padding: 0.22rem 0.6rem;
    border: 1px solid var(--line-strong);
    color: var(--ink-3);
    background: transparent;
    font-size: 11px;
    border-radius: var(--r-xs);
    cursor: pointer;
  }
  .root-chip.active { background: var(--ink); color: var(--cream); border-color: var(--ink); }
  .root-chip:hover:not(.active) { background: var(--cream-2); }

  .path { display: flex; align-items: center; gap: 0.4rem; font-size: 12px; }
  .up {
    width: 1.6rem; height: 1.6rem;
    border: 1px solid var(--line-strong);
    border-radius: var(--r-xs);
    background: transparent;
    color: var(--ink-3);
    cursor: pointer;
  }
  .up:hover:not(:disabled) { color: var(--coral-dark); border-color: var(--coral); }
  .up:disabled { opacity: 0.35; cursor: not-allowed; }
  .cur { color: var(--coral-dark); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

  .list { list-style: none; padding: 0; margin: 0; display: flex; flex-direction: column; gap: 0.05rem; }
  .row {
    display: grid;
    grid-template-columns: 0.9rem 1fr auto;
    align-items: center;
    gap: 0.5rem;
    padding: 0.4rem 0.6rem;
    font-family: var(--font-mono);
    font-size: 12px;
    text-align: left;
    color: var(--ink);
    background: transparent;
    border: none;
    cursor: pointer;
    border-left: 2px solid transparent;
    transition: background 100ms ease, border-color 100ms ease;
    width: 100%;
  }
  .row:hover { background: var(--cream-2); }
  .row.active { background: var(--cream-2); border-left-color: var(--coral); color: var(--coral-deep); }
  .ic { color: var(--ink-faint); width: 0.9rem; text-align: center; }
  .name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .meta { color: var(--ink-faint); font-size: 11px; }
  .empty { padding: 1rem; color: var(--ink-faint); font-size: 11px; text-align: center; letter-spacing: 0.04em; }
  .err { color: var(--danger); font-size: 12px; margin: 0; }

  .main { display: grid; grid-template-rows: auto 1fr; min-height: 0; }
  .top {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    padding: 0.75rem 1.25rem;
    border-bottom: 1px solid var(--line);
    background: var(--cream);
  }
  .crumbs { font-size: 12px; color: var(--ink); display: flex; align-items: center; gap: 0.5rem; flex-wrap: wrap; }
  .crumbs .id { color: var(--coral-dark); }
  .crumbs .sep { color: var(--ink-faint); }
  .ink-3 { color: var(--ink-3); }
  .warn { color: var(--amber); font-size: 11px; }

  .primary {
    font-family: var(--font-mono);
    color: var(--cream);
    background: var(--ink);
    padding: 0.4rem 0.85rem;
    border-radius: var(--r-xs);
    border: 1px solid var(--ink);
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 12px;
    cursor: pointer;
  }
  .primary:hover:not(:disabled) { background: var(--coral-dark); border-color: var(--coral-dark); }
  .primary:disabled { opacity: 0.4; cursor: not-allowed; }
  .primary .kbd {
    background: rgba(255,255,255,0.1);
    border-color: rgba(255,255,255,0.25);
    color: var(--cream);
  }

  .editor {
    flex: 1;
    border: none;
    background: var(--cream);
    color: var(--ink);
    font-family: var(--font-mono);
    font-size: 13px;
    line-height: 1.55;
    padding: 1rem 1.25rem 1.5rem;
    outline: none;
    resize: none;
    overflow: auto;
  }

  .empty-area { display: grid; place-items: center; padding: 3rem 1.25rem; }
  .empty-card {
    border: 1px dashed var(--line-strong);
    padding: 2rem 2.5rem;
    text-align: center;
    max-width: 32rem;
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

  @media (max-width: 800px) {
    .layout { grid-template-columns: minmax(0, 1fr); }
    .side { display: none; }
  }
</style>
