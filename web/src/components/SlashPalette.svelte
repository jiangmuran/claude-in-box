<script lang="ts">
  import { T } from '../lib/i18n'

  // Slash commands the user can quickly insert. Built-in claude REPL
  // commands plus a couple of patterns we know are widely used in
  // .claude/commands/*.md custom skill libraries.
  type Cmd = { name: string; en: string; zh: string }
  const builtin: Cmd[] = [
    { name: '/help',     en: 'show all commands',         zh: '查看全部命令' },
    { name: '/clear',    en: 'clear conversation context',zh: '清空当前会话上下文' },
    { name: '/compact',  en: 'compact transcript',        zh: '压缩历史' },
    { name: '/init',     en: 'generate CLAUDE.md for cwd',zh: '为当前目录生成 CLAUDE.md' },
    { name: '/resume',   en: 'pick a previous session',   zh: '挑一个旧 session 续上' },
    { name: '/model',    en: 'switch model live',         zh: '当场切换模型' },
    { name: '/effort',   en: 'switch thinking depth',     zh: '切换思考深度' },
    { name: '/cost',     en: 'show this session\'s usage',zh: '查看本会话用量' },
    { name: '/agents',   en: 'manage subagents',          zh: '管理子代理' },
    { name: '/skill',    en: 'invoke a skill by name',    zh: '按名启动 skill' },
    { name: '/theme',    en: 'change the colour theme',   zh: '改主题' },
    { name: '/memory',   en: 'show / edit memory',        zh: '查看 / 编辑记忆' },
    { name: '/mcp',      en: 'manage MCP servers',        zh: 'MCP 服务' },
    { name: '/loop',     en: 'run a loop (user skill)',   zh: '跑一个循环(自定义 skill)' },
    { name: '/goal',     en: 'set a goal (user skill)',   zh: '设定目标(自定义 skill)' },
  ]

  interface Props {
    query: string
    onpick: (cmd: string) => void
    onclose: () => void
  }
  let { query, onpick, onclose }: Props = $props()

  let filtered = $derived(
    builtin.filter((c) => c.name.toLowerCase().includes(query.toLowerCase()))
  )
  let active = $state(0)
  $effect(() => {
    if (active >= filtered.length) active = 0
  })

  function handleKey(e: KeyboardEvent) {
    if (filtered.length === 0) return
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      active = (active + 1) % filtered.length
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      active = (active - 1 + filtered.length) % filtered.length
    } else if (e.key === 'Tab' || e.key === 'Enter') {
      e.preventDefault()
      onpick(filtered[active].name + ' ')
    } else if (e.key === 'Escape') {
      e.preventDefault()
      onclose()
    }
  }
</script>

<svelte:window onkeydown={handleKey} />

<div class="palette" role="listbox" aria-label={$T('slash commands', '斜线命令')}>
  <div class="palette-head mono">{$T('slash commands', '斜线命令')}</div>
  {#if filtered.length === 0}
    <div class="palette-empty mono">{$T('no matches', '没有匹配项')}</div>
  {/if}
  {#each filtered as c, i (c.name)}
    <button
      type="button"
      class="palette-row"
      class:active={i === active}
      onmousedown={(e) => { e.preventDefault(); onpick(c.name + ' ') }}
      onmouseenter={() => (active = i)}
    >
      <span class="cmd mono">{c.name}</span>
      <span class="desc">{$T(c.en, c.zh)}</span>
    </button>
  {/each}
  <div class="palette-hint mono">
    {$T('↑↓ navigate · ⇥ / ↵ insert · esc cancel', '↑↓ 选择 · ⇥/↵ 插入 · esc 取消')}
  </div>
</div>

<style>
  .palette {
    position: absolute;
    bottom: 100%;
    left: 1rem;
    right: 1rem;
    margin-bottom: 0.4rem;
    background: var(--cream);
    border: 1px solid var(--line-strong);
    box-shadow: var(--shadow-2);
    border-radius: var(--r-xs);
    z-index: 5;
    max-height: 18rem;
    overflow-y: auto;
    animation: rise 160ms cubic-bezier(.2,.8,.2,1) both;
  }
  .palette-head {
    padding: 0.4rem 0.75rem;
    font-size: 10px;
    letter-spacing: 0.12em;
    text-transform: uppercase;
    color: var(--ink-faint);
    border-bottom: 1px dashed var(--line);
  }
  .palette-empty {
    padding: 0.85rem;
    color: var(--ink-faint);
    font-size: 12px;
    text-align: center;
  }
  .palette-row {
    width: 100%;
    display: grid;
    grid-template-columns: 6.5rem 1fr;
    column-gap: 0.75rem;
    align-items: baseline;
    padding: 0.45rem 0.75rem;
    background: transparent;
    border: none;
    border-left: 2px solid transparent;
    text-align: left;
    cursor: pointer;
    color: var(--ink);
    transition: background 80ms ease, border-color 80ms ease;
  }
  .palette-row:hover,
  .palette-row.active {
    background: var(--cream-2);
    border-left-color: var(--coral);
  }
  .palette-row .cmd { color: var(--coral-dark); font-size: 12px; }
  .palette-row .desc { color: var(--ink-3); font-size: 12px; font-family: var(--font-display); }
  .palette-hint {
    padding: 0.4rem 0.75rem;
    border-top: 1px dashed var(--line);
    color: var(--ink-faint);
    font-size: 10px;
    letter-spacing: 0.06em;
  }
</style>
