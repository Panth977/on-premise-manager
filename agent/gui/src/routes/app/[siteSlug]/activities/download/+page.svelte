<script lang="ts">
  import NumberStepper from '$lib/ui/NumberStepper.svelte';
  import { base } from '$app/paths';
  import { page } from '$app/state';
  import { api, apiPath, download, errMessage } from '$lib/api';
  import { overview } from '$lib/state/app.svelte';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toast.svelte';
  import { jsonPretty } from '$lib/format';
  import PageHead from '$lib/ui/PageHead.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';

  const slug = $derived(page.params.siteSlug ?? '*');
  let from = $state<number | null>(1);
  let to = $state<number | null>(null);
  let includeDebug = $state(false);
  let lastSeq = $state<number | null>(null);
  let busy = $state(false);
  let error = $state<string | null>(null);

  $effect(() => {
    if (slug === '*') {
      lastSeq = null;
      return;
    }
    api
      .siteActivities({ siteSlug: slug, query: { limit: 1 } })
      .then((r) => (lastSeq = r.items[0]?.seq ?? 0))
      .catch((e) => (error = errMessage(e)));
  });
  const valid = $derived((from === null || (Number.isInteger(from) && from >= 1)) && (to === null || (Number.isInteger(to) && to >= 1)) && (from === null || to === null || from <= to));
  const dep = $derived(overview.data?.deploymentId ?? session.info?.deploymentId ?? '<deploymentId>');
  const fileName = $derived(`${dep}-${slug === '*' ? 'all' : slug}-${from ?? 1}-${to ?? lastSeq ?? 'last'}.opmlog`);
  const manifest = $derived({
    format: 'opmlog/1',
    deploymentId: dep,
    createdBy: session.actor?.email ?? null,
    signedBy: { siteSlug: overview.data?.self ?? '?' },
    sites: (slug === '*' ? (overview.data?.sites.filter((s) => s.reachable).map((s) => s.siteSlug) ?? []) : [slug]).map((s) => ({ siteSlug: s, fromSeq: from ?? 1, toSeq: to ?? (slug === '*' ? 'last' : (lastSeq ?? 'last')) })),
    activities: { path: 'activities.ndjson' },
    debug: includeDebug ? 'debug/<site>.jsonl for every site' : [],
  });
  async function go(e: SubmitEvent) {
    e.preventDefault();
    if (!valid) return;
    busy = true;
    error = null;
    try {
      await download('downloadActivities', apiPath('downloadActivities', { siteSlug: slug }, { from: from ?? undefined, to: to ?? undefined, includeDebug: includeDebug || undefined }), fileName);
      toasts.ok('Bundle downloaded', fileName);
    } catch (err) {
      error = errMessage(err);
    } finally {
      busy = false;
    }
  }
</script>

<PageHead icon="download" eyebrow={slug === '*' ? 'All sites' : `Site ${slug}`} title="Download activity bundle" sub="A signed .opmlog file of the activity log (optional debug log), for sites without internet.">
  {#snippet actions()}<a class="btn" href="{base}/app/{slug}/activities"><Icon name="chevronLeft" size={14} />Activities</a>{/snippet}
</PageHead>

<div class="grid cols-2" style="align-items:start">
  <form class="card card-pad stack" onsubmit={go}>
    <div class="form-grid">
      <div class="field">
        <label for="d-from">From seq</label>
        <NumberStepper id="d-from" nullable min={1} max={lastSeq ?? undefined} placeholder="1" value={from} onchange={(v) => (from = v)} label="from seq" />
        <span class="hint">inclusive · default 1</span>
      </div>
      <div class="field">
        <label for="d-to">To seq</label>
        <NumberStepper id="d-to" nullable min={1} max={lastSeq ?? undefined} placeholder={lastSeq !== null ? String(lastSeq) : 'last'} value={to} onchange={(v) => (to = v)} label="to seq" />
        <span class="hint">inclusive · default the last entry{lastSeq !== null ? ` (${lastSeq})` : ''}</span>
      </div>
    </div>
    {#if !valid}<div class="field-err">From must be ≤ To, both whole numbers ≥ 1.</div>{/if}
    <label class="check"><input type="checkbox" bind:checked={includeDebug} />Include debug log (never contains secret values; the download is logged)</label>
    {#if error}<div class="banner bad small" role="alert"><Icon name="alert" /><div class="grow">{error}</div></div>{/if}
    <div class="row">
      <Btn type="submit" variant="primary" icon="download" {busy} disabled={!valid} data-testid="download-opmlog">Download .opmlog</Btn>
      <span class="mono small muted">{fileName}</span>
    </div>
    <div class="banner info small"><Icon name="info" /><div class="grow">Send the file to the operations team; they upload it at <code>/app/deployments/{dep}/activities/upload</code> in the cloud. The bundle is signed by this site ({overview.data?.self ?? '?'}).</div></div>
  </form>
  <section class="card">
    <div class="card-head"><h2>Manifest preview</h2><span class="muted small">what the signed manifest.json will list</span></div>
    <div class="card-body"><pre class="code" data-testid="manifest-preview">{jsonPretty(manifest)}</pre></div>
  </section>
</div>
