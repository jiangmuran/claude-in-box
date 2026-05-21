<script lang="ts">
  import { onMount, onDestroy } from 'svelte'
  import { Terminal } from '@xterm/xterm'
  import { FitAddon } from '@xterm/addon-fit'
  import { WebLinksAddon } from '@xterm/addon-web-links'
  import { get } from 'svelte/store'
  import { authToken } from '../lib/stores'

  interface Props { shellId: string }
  let { shellId }: Props = $props()

  let host = $state<HTMLDivElement | null>(null)
  let term: Terminal | null = null
  let fit: FitAddon | null = null
  let ws: WebSocket | null = null
  let ro: ResizeObserver | null = null

  function open() {
    if (!host) return
    term = new Terminal({
      fontFamily: 'JetBrains Mono Variable, JetBrains Mono, Menlo, monospace',
      fontSize: 13,
      lineHeight: 1.35,
      cursorBlink: true,
      cursorStyle: 'block',
      allowProposedApi: true,
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

    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const tok = get(authToken)
    const url = `${proto}//${location.host}/ws/shells/${shellId}`
    ws = new WebSocket(url, tok ? [`bearer.${tok}`, 'binary'] : ['binary'])
    ws.binaryType = 'arraybuffer'

    ws.onopen = () => {
      // Push initial size.
      const cols = term?.cols ?? 120
      const rows = term?.rows ?? 32
      ws?.send(JSON.stringify({ resize: { cols, rows } }))
    }
    ws.onmessage = (ev) => {
      if (!term) return
      if (ev.data instanceof ArrayBuffer) {
        term.write(new Uint8Array(ev.data))
      } else if (typeof ev.data === 'string') {
        term.write(ev.data)
      }
    }
    ws.onclose = () => {
      term?.writeln('\r\n\x1b[31m— shell ended —\x1b[0m')
    }

    term.onData((data) => {
      if (ws?.readyState === WebSocket.OPEN) {
        ws.send(new TextEncoder().encode(data))
      }
    })

    term.onResize(({ cols, rows }) => {
      if (ws?.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ resize: { cols, rows } }))
      }
    })

    ro = new ResizeObserver(() => fit?.fit())
    ro.observe(host)
  }

  onMount(open)

  onDestroy(() => {
    ro?.disconnect()
    if (ws && ws.readyState === WebSocket.OPEN) ws.close()
    term?.dispose()
  })
</script>

<div class="wrap">
  <div bind:this={host} class="term"></div>
</div>

<style>
  .wrap {
    flex: 1;
    display: flex;
    flex-direction: column;
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
</style>
