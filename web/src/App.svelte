<script lang="ts">
  import { authToken } from './lib/stores'
  import Onboarding from './routes/Onboarding.svelte'
  import Dashboard from './routes/Dashboard.svelte'

  let token = $state('')
  authToken.subscribe((v) => (token = v))
</script>

{#if token}
  <Dashboard />
{:else}
  <Onboarding />
{/if}

<style>
  :global(#app) {
    min-height: 100vh;
    display: block;
  }
</style>
