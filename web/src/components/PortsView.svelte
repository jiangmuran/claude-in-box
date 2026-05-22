<script lang="ts">
  import { onMount, onDestroy } from 'svelte'
  import { api, ApiError } from '../lib/api'
  import { T } from '../lib/i18n'

  type Mapping = {
    host_port: number
    internal_port: number
    internal_host?: string
    created_at: string
  }

  let mappings = $state<Mapping[]>([])
  let range = $state<[number, number]>([0, 0])
  let configured = $derived(range[0] > 0)
  let error = $state('')
  let busy = $state(false)
  let timer: ReturnType<typeof setInterval> | null = null

  // Form
  let internalPort = $state<number | ''>('')
  let internalHost = $state('127.0.0.1')

  async function refresh() {
    try {
      const r = await api.listPorts()
      mappings = r.mappings ?? []
      range = r.range ?? [0, 0]
      error = ''
    } catch (e) {
      // 503 = ports not configured; surface as a soft state, not an error.
      if (e instanceof ApiError && e.message.includes('ports not configured')) {
        range = [0, 0]
        mappings = []
        error = ''
        return
      }
      error = e instanceof ApiError ? e.message : (e as Error).message
    }
  }

  async function expose() {
    if (!internalPort || internalPort < 1) return
    busy = true; error = ''
    try {
      await api.exposePort(Number(internalPort), internalHost || undefined)
      internalPort = ''
      await refresh()
    } catch (e) {
      error = e instanceof ApiError ? e.message : (e as Error).message
    } finally {
      busy = false
    }
  }

  async function unexpose(hostPort: number) {
    try {
      await api.unexposePort(hostPort)
      await refresh()
    } catch (e) {
      error = e instanceof ApiError ? e.message : (e as Error).message
    }
  }

  onMount(() => {
    refresh()
    timer = setInterval(refresh, 4000)
  })
  onDestroy(() => { if (timer) clearInterval(timer) })
</script>

<div class="wrap">
  <header class="head">
    <span class="divider">{$T('port mapping', '端口映射')}</span>
    {#if configured}
      <span class="mono ink-3">{range[0]}–{range[1]}</span>
    {/if}
  </header>

  {#if !configured}
    <div class="empty">
      <p class="serif">{$T(
        'No host port range allocated. Restart the container with',
        '没有可用的宿主端口段。容器启动时加上'
      )}</p>
      <pre class="mono cmd">docker run -e CIB_PORT_RANGE=9000-9019 -p 9000-9019:9000-9019 …</pre>
      <p class="serif ink-3">{$T(
        'then anything inside the container (vite, fastapi, jupyter…) can be exposed on demand without rebuilding the run line.',
        '之后容器里任何服务(vite、fastapi、jupyter…)都可以按需对外暴露,不需要重启容器。'
      )}</p>
    </div>
  {:else}
    <form class="add" onsubmit={(e) => { e.preventDefault(); expose() }}>
      <label class="field">
        <span class="lbl mono">{$T('internal port', '容器内端口')}</span>
        <input type="number" min="1" max="65535" bind:value={internalPort} placeholder="5173" class="mono" />
      </label>
      <label class="field">
        <span class="lbl mono">{$T('internal host', '容器内地址')}</span>
        <input bind:value={internalHost} placeholder="127.0.0.1" class="mono" />
      </label>
      <button class="primary" type="submit" disabled={!internalPort || busy}>
        {busy ? $T('exposing…', '映射中…') : $T('expose', '映射')}
      </button>
    </form>

    {#if mappings.length === 0}
      <p class="empty-list mono">{$T('— no active mappings —', '— 暂无映射 —')}</p>
    {:else}
      <table class="grid">
        <thead>
          <tr>
            <th class="mono">{$T('host', '宿主')}</th>
            <th class="mono">↦</th>
            <th class="mono">{$T('container', '容器')}</th>
            <th class="mono">{$T('since', '建立时间')}</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {#each mappings as m (m.host_port)}
            <tr>
              <td class="mono num">{m.host_port}</td>
              <td class="mono arrow">↦</td>
              <td class="mono num">{m.internal_host ?? '127.0.0.1'}:{m.internal_port}</td>
              <td class="mono time">{new Date(m.created_at).toLocaleString()}</td>
              <td><button class="ghost danger" onclick={() => unexpose(m.host_port)}>{$T('close', '关闭')}</button></td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  {/if}

  {#if error}
    <p class="err mono">[ {error} ]</p>
  {/if}
</div>

<style>
  .wrap {
    flex: 1;
    overflow-y: auto;
    padding: 1.25rem clamp(0.5rem, 3vw, 2rem) 2rem;
    display: flex;
    flex-direction: column;
    gap: 1rem;
    max-width: 64rem;
    margin: 0 auto;
    width: 100%;
  }
  .head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 0.5rem;
  }
  .head .ink-3 { color: var(--ink-3); font-size: 12px; }

  .empty {
    border: 1px dashed var(--line-strong);
    padding: 1.5rem;
    display: flex;
    flex-direction: column;
    gap: 0.85rem;
    color: var(--ink-2);
  }
  .empty .cmd {
    background: var(--cream-2);
    border: 1px solid var(--line);
    padding: 0.65rem 0.85rem;
    overflow-x: auto;
    font-size: 0.85rem;
    color: var(--ink);
  }
  .empty .ink-3 { color: var(--ink-3); font-size: 0.95rem; }

  .add {
    display: grid;
    grid-template-columns: 1fr 1fr auto;
    align-items: end;
    gap: 0.85rem;
    border: 1px solid var(--line-strong);
    padding: 1rem;
    background: var(--cream);
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 0.3rem;
  }
  .field .lbl {
    font-size: 10px;
    letter-spacing: 0.12em;
    text-transform: uppercase;
    color: var(--ink-3);
  }
  .field input {
    border: 1px solid var(--line-strong);
    background: var(--cream-2);
    padding: 0.45rem 0.6rem;
    font-size: 0.92rem;
    color: var(--ink);
    border-radius: var(--r-xs);
  }
  .field input:focus { outline: none; border-color: var(--coral); }

  .empty-list {
    color: var(--ink-faint);
    text-align: center;
    padding: 1.5rem;
    border: 1px dashed var(--line);
  }

  .grid {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
  }
  .grid th, .grid td {
    padding: 0.45rem 0.55rem;
    border-bottom: 1px solid var(--line);
    text-align: left;
    vertical-align: baseline;
  }
  .grid th {
    color: var(--ink-3);
    font-size: 10px;
    letter-spacing: 0.1em;
    text-transform: uppercase;
    border-bottom-color: var(--line-strong);
  }
  .grid .num { color: var(--ink); }
  .grid .arrow { color: var(--coral); text-align: center; }
  .grid .time { color: var(--ink-faint); font-size: 12px; }

  .err { color: var(--danger); font-size: 12px; }
</style>
