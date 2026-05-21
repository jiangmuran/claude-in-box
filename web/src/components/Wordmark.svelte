<script lang="ts">
  interface Props { size?: number }
  let { size = 32 }: Props = $props()
</script>

<span class="wm" style="--s: {size}px">
  <span class="glyph" aria-hidden="true">
    <svg viewBox="0 0 32 32" width={size} height={size}>
      <rect x="2" y="2" width="28" height="28" rx="6" fill="#D97757" />
      <!-- sparkle -->
      <g fill="#F5F0E8" transform="translate(16 16)">
        <rect x="-9" y="-1.5" width="18" height="3" rx="1" transform="rotate(0)" />
        <rect x="-9" y="-1.5" width="18" height="3" rx="1" transform="rotate(60)" />
        <rect x="-9" y="-1.5" width="18" height="3" rx="1" transform="rotate(120)" />
      </g>
      <!-- cursor underscore -->
      <rect x="20" y="22" width="6" height="2" rx="1" fill="#F5F0E8" />
    </svg>
  </span>
  <span class="text">claude-in-box</span>
</span>

<style>
  .wm {
    display: inline-flex;
    align-items: center;
    gap: 0.65em;
    font-family: var(--font-mono);
    font-weight: 600;
    letter-spacing: -0.01em;
    font-size: calc(var(--s) * 0.55);
    color: var(--coral-dark);
    line-height: 1;
  }
  .glyph { display: inline-flex; }
  .text { white-space: nowrap; }
</style>
