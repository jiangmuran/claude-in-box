<script lang="ts">
  import { chatFrames } from '../lib/stores'
  import type { Frame } from '../lib/types'

  // Collapse consecutive same-role text.delta frames into single messages,
  // mirroring claude-code-webui's UnifiedMessageProcessor turn handling.
  type Bubble =
    | { type: 'text'; seq: number; role: 'user' | 'assistant'; text: string }
    | { type: 'thinking'; seq: number; text: string }
    | { type: 'tool'; seq: number; toolUseId?: string; tool: string; input?: unknown; result?: unknown; error?: string; isError?: boolean; durationMs?: number }
    | { type: 'todo'; seq: number; items: { subject: string; status: string; activeForm?: string }[] }
    | { type: 'ask'; seq: number; prompt: string; options: { label: string; description?: string }[]; multi: boolean }
    | { type: 'meta'; seq: number; note: string }

  function aggregate(frames: Frame[]): Bubble[] {
    const out: Bubble[] = []
    let buf: Bubble | null = null
    function flush() { if (buf) { out.push(buf); buf = null } }

    // Tool join cache: tool_use_id → bubble index in `out`, so a late
    // tool.use.result can hydrate the original tool bubble even if other
    // bubbles slid in between.
    const toolIdx = new Map<string, number>()

    for (const f of frames) {
      switch (f.kind) {
        case 'text.delta': {
          const d = f.data as { text?: string; role?: 'user' | 'assistant' }
          const text = d?.text ?? ''
          const role = (d?.role === 'user' ? 'user' : 'assistant')
          if (buf && buf.type === 'text' && buf.role === role) {
            buf.text += text
          } else {
            flush()
            buf = { type: 'text', seq: f.seq, role, text }
          }
          break
        }
        case 'thinking': {
          flush()
          const d = f.data as { text?: string }
          out.push({ type: 'thinking', seq: f.seq, text: d?.text ?? '' })
          break
        }
        case 'tool.use.start': {
          flush()
          const d = f.data as { tool?: string; input?: unknown; tool_use_id?: string }
          const b: Bubble = { type: 'tool', seq: f.seq, toolUseId: d?.tool_use_id, tool: d?.tool ?? 'tool', input: d?.input }
          out.push(b)
          if (d?.tool_use_id) toolIdx.set(d.tool_use_id, out.length - 1)
          break
        }
        case 'tool.use.result': {
          flush()
          const d = f.data as { tool?: string; output?: unknown; error?: string; is_error?: boolean; duration_ms?: number; tool_use_id?: string }
          const at = d?.tool_use_id ? toolIdx.get(d.tool_use_id) : undefined
          if (at != null && out[at]?.type === 'tool') {
            const t = out[at] as Extract<Bubble, { type: 'tool' }>
            t.result = d?.output
            t.error = d?.error
            t.isError = d?.is_error
            t.durationMs = d?.duration_ms
          } else {
            out.push({ type: 'tool', seq: f.seq, toolUseId: d?.tool_use_id, tool: d?.tool ?? 'tool', result: d?.output, error: d?.error, isError: d?.is_error, durationMs: d?.duration_ms })
          }
          break
        }
        case 'todo.update': {
          flush()
          const d = f.data as { items?: { subject?: string; content?: string; status?: string; activeForm?: string }[] }
          const items = (d?.items ?? []).map((t) => ({
            subject: t.subject ?? t.content ?? '',
            status: t.status ?? 'pending',
            activeForm: t.activeForm,
          }))
          out.push({ type: 'todo', seq: f.seq, items })
          break
        }
        case 'ask.question': {
          flush()
          const d = f.data as { prompt?: string; options?: { label: string; description?: string }[]; multi_select?: boolean }
          out.push({ type: 'ask', seq: f.seq, prompt: d?.prompt ?? '', options: d?.options ?? [], multi: !!d?.multi_select })
          break
        }
        case 'meta': {
          flush()
          const d = f.data as { note?: string; model?: string }
          const note = d?.model ? `model · ${d.model}` : (d?.note ?? '—')
          out.push({ type: 'meta', seq: f.seq, note })
          break
        }
        default: break
      }
    }
    flush()
    return out
  }

  let bubbles = $derived(aggregate($chatFrames))

  function fmt(input: unknown): string {
    if (input == null) return ''
    if (typeof input === 'string') return input
    try { return JSON.stringify(input, null, 2) } catch { return String(input) }
  }
</script>

<div class="scroll">
  <div class="thread">
    {#if bubbles.length === 0}
      <div class="quiet">
        <span class="divider">waiting</span>
        <p class="serif">
          When Claude starts producing text, tool calls or todos, they arrive here as
          a chat-style transcript — assembled live from the typed event stream.
        </p>
      </div>
    {/if}

    {#each bubbles as b (b.seq)}
      {#if b.type === 'text'}
        <article class="bubble text" class:user={b.role === 'user'} class:assistant={b.role === 'assistant'}>
          <div class="bubble-head">
            <span class="who">{b.role === 'user' ? 'you' : 'claude'}</span>
            <span class="seq mono">#{b.seq}</span>
          </div>
          <div class="bubble-body serif">{b.text}</div>
        </article>
      {:else if b.type === 'thinking'}
        <article class="bubble thinking">
          <div class="bubble-head"><span class="who">thinking</span><span class="seq mono">#{b.seq}</span></div>
          <div class="bubble-body serif">{b.text}</div>
        </article>
      {:else if b.type === 'todo'}
        <article class="bubble todo">
          <div class="bubble-head"><span class="who">todos</span><span class="seq mono">#{b.seq}</span></div>
          <ul class="todo-list">
            {#each b.items as t, i (i)}
              <li class={'todo-' + t.status}>
                <span class="tick mono">{t.status === 'completed' ? '✓' : t.status === 'in_progress' ? '⏵' : '·'}</span>
                <span class="todo-text">{t.status === 'in_progress' && t.activeForm ? t.activeForm : t.subject}</span>
              </li>
            {/each}
          </ul>
        </article>
      {:else if b.type === 'tool'}
        <article class="bubble tool" class:errored={b.error}>
          <div class="bubble-head">
            <span class="tool-name mono">tool · {b.tool}</span>
            {#if b.durationMs != null}<span class="dur mono">{b.durationMs}ms</span>{/if}
            <span class="seq mono">#{b.seq}</span>
          </div>
          {#if b.input != null}
            <details>
              <summary class="mono">input</summary>
              <pre class="mono">{fmt(b.input)}</pre>
            </details>
          {/if}
          {#if b.result != null}
            <details open>
              <summary class="mono">output</summary>
              <pre class="mono">{fmt(b.result)}</pre>
            </details>
          {/if}
          {#if b.error}
            <p class="err mono">[ {b.error} ]</p>
          {/if}
        </article>
      {:else if b.type === 'ask'}
        <article class="bubble ask">
          <div class="bubble-head"><span class="who">claude · asks</span><span class="seq mono">#{b.seq}</span></div>
          <div class="bubble-body serif">{b.prompt}</div>
          <ul class="options">
            {#each b.options as opt, i (i)}
              <li><span class="mono key">{i + 1}</span><span class="opt">{opt.label}</span>{#if opt.description}<span class="opt-desc">{opt.description}</span>{/if}</li>
            {/each}
          </ul>
          <p class="hint mono">— reply by sending the option number via the input bar —</p>
        </article>
      {:else}
        <article class="bubble meta">
          <span class="mono">— {b.note} —</span>
        </article>
      {/if}
    {/each}
  </div>
</div>

<style>
  .scroll {
    flex: 1;
    overflow-y: auto;
    padding: 1.25rem clamp(0.5rem, 3vw, 2rem) 2rem;
  }
  .thread {
    display: flex;
    flex-direction: column;
    gap: 1rem;
    max-width: 56rem;
    margin: 0 auto;
  }

  .quiet {
    border: 1px dashed var(--line-strong);
    padding: 1.5rem;
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
    text-align: center;
    color: var(--ink-3);
  }
  .quiet p { margin: 0; max-width: 36rem; align-self: center; font-variation-settings: 'opsz' 14, 'SOFT' 70; }

  .bubble {
    border: 1px solid var(--line);
    background: var(--cream);
    padding: 0.85rem 1rem;
    position: relative;
    animation: rise 200ms ease both;
  }
  .bubble-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 0.5rem;
    margin-bottom: 0.55rem;
  }
  .who {
    font-family: var(--font-mono);
    font-size: 11px;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--coral-dark);
  }
  .who::before { content: '['; opacity: 0.5; padding-right: 4px; }
  .who::after  { content: ']'; opacity: 0.5; padding-left: 4px; }
  .seq { font-size: 11px; color: var(--ink-faint); }

  .bubble-body {
    font-size: 1rem;
    line-height: 1.6;
    color: var(--ink);
    font-variation-settings: 'opsz' 14, 'SOFT' 70;
    white-space: pre-wrap;
  }

  .bubble.text.user {
    background: linear-gradient(180deg, rgba(217,119,87,0.10), rgba(217,119,87,0.04));
    border-color: var(--coral);
    margin-left: 2rem;
  }
  .bubble.text.user .who { color: var(--coral-deep); }
  .bubble.text.assistant {
    background: var(--cream);
    margin-right: 2rem;
  }

  .bubble.thinking {
    background: var(--cream-2);
    border-color: var(--line);
    border-style: dashed;
  }
  .bubble.thinking .who { color: var(--ink-3); }
  .bubble.thinking .bubble-body { color: var(--ink-3); font-style: italic; font-size: 0.92rem; }

  .bubble.todo { background: var(--cream-2); border-color: var(--line-strong); }
  .bubble.todo .who { color: var(--coral-dark); }
  .todo-list { list-style: none; padding: 0; margin: 0; display: grid; gap: 0.3rem; }
  .todo-list li { display: grid; grid-template-columns: 1.2rem 1fr; align-items: baseline; font-family: var(--font-mono); font-size: 13px; }
  .todo-list .tick { color: var(--ink-faint); }
  .todo-list .todo-completed { color: var(--ink-faint); text-decoration: line-through; }
  .todo-list .todo-completed .tick { color: var(--ok); }
  .todo-list .todo-in_progress { color: var(--coral-dark); }
  .todo-list .todo-in_progress .tick { color: var(--coral); }

  .bubble.tool { background: var(--cream-2); border-color: var(--line-strong); }
  .tool-name { color: var(--ink-2); font-weight: 500; }
  .dur { color: var(--ink-faint); font-size: 11px; }
  details summary {
    cursor: pointer;
    font-size: 11px;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--ink-3);
    padding: 0.35rem 0;
    list-style: none;
  }
  details summary::-webkit-details-marker { display: none; }
  details summary::before { content: '▸ '; opacity: 0.6; }
  details[open] summary::before { content: '▾ '; }
  details pre {
    margin: 0.25rem 0 0.5rem;
    background: var(--cream);
    border: 1px solid var(--line);
    padding: 0.5rem 0.65rem;
    font-size: 0.8rem;
    line-height: 1.5;
    color: var(--ink-2);
    max-height: 22rem;
    overflow: auto;
    white-space: pre-wrap;
    word-break: break-word;
  }
  .bubble.errored { border-color: var(--danger); }
  .err { color: var(--danger); font-size: 12px; margin-top: 0.4rem; }

  .bubble.ask {
    background: linear-gradient(180deg, rgba(217,119,87,0.06), transparent);
    border-color: var(--coral);
  }
  .options {
    list-style: none;
    margin: 0.65rem 0 0;
    padding: 0;
    display: grid;
    gap: 0.4rem;
  }
  .options li {
    display: grid;
    grid-template-columns: 1.5rem 1fr;
    gap: 0.65rem;
    align-items: baseline;
  }
  .options .key {
    width: 1.4rem;
    height: 1.4rem;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    font-size: 11px;
    border: 1px solid var(--line-strong);
    border-radius: var(--r-xs);
    color: var(--coral-dark);
  }
  .opt-desc {
    grid-column: 2;
    color: var(--ink-3);
    font-size: 0.9em;
    font-family: var(--font-mono);
  }
  .hint { color: var(--ink-faint); font-size: 11px; margin-top: 0.7rem; }

  .bubble.meta {
    border-style: dashed;
    text-align: center;
    color: var(--ink-faint);
    background: transparent;
    padding: 0.4rem;
    font-size: 12px;
    letter-spacing: 0.1em;
  }
</style>
