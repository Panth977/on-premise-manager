<script lang="ts">
  import { page } from '$app/state';
  import { base } from '$app/paths';
</script>

<svelte:head><title>OPM agent · page not found</title></svelte:head>
<main class="nf">
  <div class="card card-pad">
    <div class="section-title">OPM agent · {page.status}</div>
    <h1>{page.status === 404 ? 'Page not found' : 'Something went wrong'}</h1>
    <p class="muted">{page.status === 404 ? `There is no agent page at ${page.url.pathname}.` : (page.error?.message ?? 'Unexpected error')}</p>
    <a class="btn primary" href="{base}/app">Back to /_opm/app</a>
  </div>
</main>

<style>
  .nf {
    min-height: 100dvh;
    display: grid;
    place-items: center;
    padding: 16px;
  }
  .card {
    max-width: 440px;
    width: 100%;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .btn {
    align-self: flex-start;
  }
</style>
