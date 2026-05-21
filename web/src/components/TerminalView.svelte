<script lang="ts">
  import { onMount, onDestroy } from 'svelte'
  import { Terminal } from '@xterm/xterm'
  import { FitAddon } from '@xterm/addon-fit'
  import { WebLinksAddon } from '@xterm/addon-web-links'
  import { frames } from '../lib/stores'
  import type { Frame } from '../lib/types'

  interface Props { sessionId: string }
  let { sessionId }: Props = $props()

  let host = $state<HTMLDivElement | null>(null)
  let term: Terminal | null = null
  let fit: FitAddon | null = null
  let lastWritten = $state(0)

  function init() {
    if (!host) return
    term = new Terminal({
      fontFamily: 'JetBrains Mono Variable, JetBrains Mono, Menlo, monospace',
      fontSize: 13,
      lineHeight: 1.35,
      cursorBlink: true,
      cursorStyle: 'block',
      allowProposedApi: true,
      // Coral-on-cream is unreadable for code; flip to a warm dark TTY.
      theme: {
        background:        '#1F1814',
        foreground:        '#E9DBC6',
        cursor:            '#D97757',
        cursorAccent:      '#1F1814',
        selectionBackground: 'rgba(217,119,87,0.35)',
        black:             '#1F1814',
        red:               '#D97757',
        green:             '#A0B074',
        yellow:            '#FFB76B',
        blue:              '#7AA0BF',
        magenta:           '#C18A9B',
        cyan:              '#88BABA',
        white:             '#E9DBC6',
        brightBlack:       '#5C4D3F',
        brightRed:         '#E89373',
        brightGreen:       '#C2D08E',
        brightYellow:      '#FFD08A',
        brightBlue:        '#A1C0D6',
        brightMagenta:     '#D4A8B5',
        brightCyan:        '#A4CFCF',
        brightWhite:       '#F5F0E8',
      },
    })
    fit = new FitAddon()
    term.loadAddon(fit)
    term.loadAddon(new WebLinksAddon())
    term.open(host)
    fit.fit()
    term.writeln('\x1b[2m— attached to session ' + sessionId + ' —\x1b[0m')
  }

  onMount(() => {
    init()
    const ro = new ResizeObserver(() => fit?.fit())
    if (host) ro.observe(host)
    return () => ro.disconnect()
  })

  onDestroy(() => term?.dispose())

  // Drain pty.raw frames into the terminal.
  frames.subscribe((fs: Frame[]) => {
    if (!term) return
    for (let i = lastWritten; i < fs.length; i++) {
      const f = fs[i]
      if (f.kind === 'pty.raw') {
        const text = (f.data as { text?: string })?.text ?? ''
        // Restore line endings — the parser strips \r\n by ScanLines.
        term.write(text + '\r\n')
      } else if (f.kind === 'cc.raw') {
        const orig = (f.data as { original?: string })?.original ?? ''
        term.write('\x1b[2m' + orig + '\x1b[0m\r\n')
      } else if (f.kind === 'stop') {
        term.writeln('\x1b[31m— session stopped —\x1b[0m')
      }
    }
    lastWritten = fs.length
  })
</script>

<div class="wrap">
  <div bind:this={host} class="term"></div>
  <p class="hint mono">— this is the raw PTY stream; switch to <span class="kbd">driver</span> for a structured chat view —</p>
</div>

<style>
  .wrap {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    padding: 0.75rem clamp(0.5rem, 2vw, 1.25rem) 1rem;
    min-height: 0;
  }
  .term {
    flex: 1;
    min-height: 0;
    background: #1F1814;
    border: 1px solid var(--ink-2);
    box-shadow: var(--shadow-1);
    padding: 0.75rem;
    position: relative;
  }
  .term::before {
    content: '';
    position: absolute;
    inset: 4px;
    border: 1px solid rgba(217, 119, 87, 0.2);
    pointer-events: none;
  }
  :global(.xterm) { height: 100%; }
  :global(.xterm .xterm-viewport) { background: transparent !important; }

  .hint {
    color: var(--ink-faint);
    font-size: 11px;
    text-align: center;
    letter-spacing: 0.04em;
  }
</style>
