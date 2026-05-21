<script lang="ts">
  import { lang, toggleLang } from '../lib/i18n'
</script>

<button
  class="lang"
  type="button"
  onclick={toggleLang}
  title={$lang === 'zh' ? 'switch to English' : '切换到中文'}
  aria-label={$lang === 'zh' ? 'switch to English' : '切换到中文'}
>
  <span class="cur mono">{$lang === 'zh' ? 'EN' : '中'}</span>
  <span class="sep">·</span>
  <span class="other mono">{$lang === 'zh' ? '中' : 'EN'}</span>
</button>

<style>
  .lang {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    padding: 0.22rem 0.55rem;
    border: 1px solid var(--line-strong);
    border-radius: var(--r-xs);
    font-size: 11px;
    color: var(--ink-3);
    background: transparent;
    transition: color 120ms ease, border-color 120ms ease, background 120ms ease;
  }
  .lang:hover {
    color: var(--coral-dark);
    border-color: var(--coral);
    background: rgba(217, 119, 87, 0.06);
  }
  .cur {
    color: var(--ink);
    font-weight: 500;
  }
  .sep {
    color: var(--ink-faint);
  }
  .other {
    color: var(--ink-faint);
  }
</style>
