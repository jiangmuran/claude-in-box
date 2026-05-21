<script lang="ts">
  import { T } from '../lib/i18n'
  import type { SessionState } from '../lib/types'
  import type { ConnectionState } from '../lib/ws'

  interface Props {
    state?: SessionState | string
    connection: ConnectionState
  }
  let { state, connection }: Props = $props()

  let key = $derived.by(() => {
    if (connection === 'connecting' || connection === 'idle') return 'connecting'
    if (connection === 'error' || connection === 'closed') return 'reconnecting'
    return state ?? 'idle'
  })

  let labels: Record<string, [string, string]> = $derived({
    connecting:        ['connecting',        '连接中'],
    reconnecting:      ['reconnecting',      '重连中'],
    starting:          ['starting',          '启动中'],
    idle:              ['idle',              '空闲'],
    working:           ['working',           '工作中'],
    waiting_for_input: ['waiting for input', '等待输入'],
    stopped:           ['stopped',           '已停止'],
    failed:            ['failed',            '失败'],
  })

  let cls = $derived(`tag tag-${String(key).replace(/_/g, '-')}`)
  let label = $derived($T(...(labels[String(key)] ?? [String(key), String(key)])))
</script>

<span class={cls}>
  <span class="d"></span>
  <span class="t mono">{label}</span>
</span>

<style>
  .tag {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.18rem 0.55rem 0.18rem 0.5rem;
    font-family: var(--font-mono);
    font-size: 11px;
    letter-spacing: 0.06em;
    background: var(--cream-2);
    border: 1px solid var(--line);
    border-radius: 999px;
    color: var(--ink-2);
  }
  .d {
    width: 7px; height: 7px;
    border-radius: 50%;
    background: var(--ink-faint);
  }
  .t::before { content: '['; opacity: 0.55; padding-right: 4px; }
  .t::after  { content: ']'; opacity: 0.55; padding-left: 4px; }

  .tag-working .d, .tag-starting .d { background: var(--amber); animation: pulse 1.2s ease-in-out infinite; }
  .tag-idle .d { background: var(--ok); }
  .tag-waiting-for-input .d { background: var(--amber); }
  .tag-stopped .d { background: var(--ink-3); }
  .tag-failed  .d { background: var(--danger); }
  .tag-connecting .d, .tag-reconnecting .d { background: var(--coral); animation: pulse 1s ease-in-out infinite; }
  .tag-working { color: var(--coral-deep); }
  .tag-failed  { color: var(--danger); }
</style>
