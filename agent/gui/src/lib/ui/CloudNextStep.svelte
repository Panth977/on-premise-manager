<script lang="ts">
  /**
   * "What happens next" for a site without a head (§21 #104): commit in the cloud console → the cloud head appears on
   * the Branch page → stages by itself → a person merges it here. Shows the console link when the agent knows it (lib/cloud.ts) and the
   * head state of this site.
   */
  import { siteHref } from '$lib/nav';
  import { base } from '$app/paths';
  import { session } from '$lib/state/session.svelte';
  import { overview } from '$lib/state/app.svelte';
  import { consoleDeploymentUrl, consoleOrigin, deploymentLabel } from '$lib/cloud';
  import Icon from './Icon.svelte';
  let { updatesLink = false }: { updatesLink?: boolean } = $props();
  const o = $derived(overview.data);
  const diffUrl = $derived(consoleDeploymentUrl(session.info, '/history'));
  const origin = $derived(consoleOrigin(session.info));
  const label = $derived(deploymentLabel(session.info));
  const self = $derived(o?.sites.find((s) => s.self));
</script>

<ol class="steps" data-testid="next-steps">
  <li>
    <span class="n">1</span>
    <span>
      In the <strong>cloud console</strong> open {#if label}the deployment&nbsp;<strong>{label}</strong>{:else}the deployment{/if} → <strong>Commit…</strong> (a commit is a release).
      {#if origin}<span class="faint xs block mono" data-testid="console-url">{origin}</span>{:else}<span class="faint xs block">The console is the web address where this deployment was created (its Setup page holds the install command).</span>{/if}
    </span>
  </li>
  <li><span class="n">2</span><span>It appears {#if updatesLink}on the <a href={siteHref('branch')}>Branch</a> page{:else}here{/if} as the cloud head and <strong>stages automatically</strong> on every site (images and files are downloaded in advance).</span></li>
  <li><span class="n">3</span><span>A person <strong>merges</strong> it here — everything or single changes, with a commit message; or <strong>fast-forwards</strong> every site at once.</span></li>
</ol>
<div class="headline" data-testid="site-head-state">
  <Icon name="server" size={14} />
  <span>This site <span class="mono strong">{o?.self ?? session.info?.siteSlug ?? '…'}</span>:</span>
  {#if o?.head}
    <span class="badge accent">head seq {o.head.seq}</span><span class="faint xs">{o.head.summary}</span>
  {:else}
    <span class="badge outline">no head yet</span><span class="faint xs">fresh install — nothing applied</span>
  {/if}
  {#if self?.staging}<span class="badge info">staging {self.staging.staging.staging}</span>{/if}
</div>
{#if diffUrl}
  <div class="row cta">
    <a class="btn primary" href={diffUrl} target="_blank" rel="noopener noreferrer" data-testid="console-diff-link"><Icon name="external" size={14} />Open the deployment in the cloud console</a>
    {#if updatesLink}<a class="btn" href={siteHref('branch')}>Open Branch</a>{/if}
  </div>
{:else if updatesLink}
  <div class="row cta"><a class="btn primary" href={siteHref('branch')}>Open Branch</a></div>
{/if}

<style>
  .steps {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .steps li {
    display: flex;
    gap: 10px;
    align-items: flex-start;
    color: var(--text-2);
  }
  .steps strong {
    color: var(--text);
    font-weight: var(--fw-medium);
  }
  .n {
    flex: none;
    width: 20px;
    height: 20px;
    border-radius: 50%;
    display: grid;
    place-items: center;
    font-size: var(--fs-xs);
    font-weight: var(--fw-semibold);
    background: var(--accent-soft);
    color: var(--accent-text);
    margin-top: 1px;
  }
  .block {
    display: block;
    margin-top: 2px;
    overflow-wrap: anywhere;
  }
  .headline {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px;
    margin-top: 14px;
    padding: 8px 10px;
    border-radius: var(--r-md);
    background: var(--surface-2);
    border: 1px solid var(--border);
    font-size: var(--fs-sm);
    color: var(--text-2);
  }
  .cta {
    margin-top: 14px;
    justify-content: center;
  }
</style>
