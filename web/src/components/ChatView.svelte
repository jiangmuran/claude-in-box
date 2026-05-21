<script lang="ts">
  import { chatFrames } from '../lib/stores'
  import type { Frame } from '../lib/types'

  // Collapse consecutive text.delta frames into single messages.
  type Bubble =
    | { type: 'text'; seq: number; text: string }
    | { type: 'tool'; seq: number; tool: string; input?: unknown; result?: unknown; error?: string; durationMs?: number }
    | { type: 'ask'; seq: number; prompt: string; options: { label: string; description?: string }[]; multi: boolean }
    | { type: 'meta'; seq: number; note: string }

  function aggregate(frames: Frame[]): Bubble[] {
    const out: Bubble[] = []
    let buf: Bubble | null = null
    function flush() { if (buf) { out.push(buf); buf = null } }

    for (const f of frames) {
      switch (f.kind) {
        case 'text.delta': {
          const text = (f.data as { text?: string })?.text ?? ''
          if (buf && buf.type === 'text') buf.text += text
          else { flush(); buf = { type: 'text', seq: f.seq, text } }
          break
        }
        case 'tool.use.start': {
          flush()
          const d = f.data as { tool?: string; input?: unknown }
          buf = { type: 'tool', seq: f.seq, tool: d?.tool ?? 'tool', input: d?.input }
          break
        }
        case 'tool.use.result': {
          // attach to the most recent tool bubble (or open a fresh one).
          const d = f.data as { tool?: string; output?: unknown; error?: string; duration_ms?: number }
          if (buf && buf.type === 'tool') {
            buf.result = d?.output
            buf.error = d?.error
            buf.durationMs = d?.duration_ms
            flush()
          } else {
            flush()
            out.push({ type: 'tool', seq: f.seq, tool: d?.tool ?? 'tool', result: d?.output, error: d?.error, durationMs: d?.duration_ms })
          }
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
          out.push({ type: 'meta', seq: f.seq, note: d?.note ?? (d?.model ? `model · ${d.model}` : '—') })
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
        <article class="bubble text">
          <div class="bubble-head"><span class="who">claude</span><span class="seq mono">#{b.seq}</span></div>
          <div class="bubble-body serif">{b.text}</div>
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
