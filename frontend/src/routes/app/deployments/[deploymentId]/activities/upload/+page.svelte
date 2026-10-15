<script lang="ts">
  import type { UploadActivitiesResponse } from '@opm/shared';
  import { api, errMsg } from '$lib/api';
  import { getDeploymentScope } from '$lib/config-editor/context';
  import { toast } from '$lib/toast.svelte';
  import { bytes } from '$lib/util/format';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Button from '$lib/ui/Button.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import { Upload, FileText, CheckCircle, CircleAlert, X, ScrollText } from '$lib/ui/icons';

  const MAX = 24 * 1024 * 1024;
  const scope = getDeploymentScope();
  const id = scope.id;
  const canWrite = $derived(scope.can('deployment.activities', 'write'));
  let file = $state<File | null>(null);
  let over = $state(false);
  let busy = $state(false);
  let error = $state<string | null>(null);
  let result = $state<UploadActivitiesResponse | null>(null);
  let input = $state<HTMLInputElement | null>(null);

  const fileErr = $derived(!file ? null : file.size > MAX ? `The file is ${bytes(file.size)}; at most 24 MiB.` : file.size === 0 ? 'The file is empty.' : !/\.opmlog$/i.test(file.name) ? 'Choose an .opmlog bundle (downloaded from the agent GUI).' : null);
  function pick(f: File | null | undefined) {
    if (!f) return;
    file = f;
    error = null;
    result = null;
  }
  function ondrop(e: DragEvent) {
    e.preventDefault();
    over = false;
    pick(e.dataTransfer?.files?.[0]);
  }
  async function upload() {
    if (!file || fileErr) return;
    busy = true;
    error = null;
    try {
      result = await api.uploadActivities({ deploymentId: id, body: file });
      toast.success('Bundle uploaded', `${result.inserted} inserted · ${result.skipped} skipped · ${result.rejected} rejected`);
    } catch (e) {
      error = errMsg(e);
      toast.error(e, 'Upload refused');
    } finally {
      busy = false;
    }
  }
  function again() {
    file = null;
    result = null;
    error = null;
    if (input) input.value = '';
  }
</script>

<svelte:head><title>Upload activities · {scope.label} · OPM</title></svelte:head>

<div class="page narrow">
  <PageHeader title="Upload an activity bundle" subtitle="A site without internet can export its activities as a signed .opmlog file (agent GUI → Activities → Download). The cloud checks the signature and the hash chains and inserts the missing entries." icon={Upload} back={{ href: `/app/deployments/${id}/activities`, label: 'Activities' }} />

  {#if !canWrite}
    <EmptyState icon={ScrollText} title="No access" text="You need deployment.activities.write to upload bundles." />
  {:else if result}
    <section class="card card-pad stack" data-testid="upload-result">
      <div class="row">
        {#if result.signatureOk}<span class="badge green"><Icon icon={CheckCircle} size={12} />signature ok</span>{:else}<span class="badge red">signature failed</span>{/if}
        <span class="muted small">{file?.name}</span>
      </div>
      <div class="grid-3 nums">
        <div class="n"><div class="v" data-testid="r-inserted">{result.inserted}</div><div class="l">inserted</div></div>
        <div class="n"><div class="v" data-testid="r-skipped">{result.skipped}</div><div class="l">skipped (already there)</div></div>
        <div class="n" class:bad={result.rejected > 0}><div class="v" data-testid="r-rejected">{result.rejected}</div><div class="l">rejected</div></div>
      </div>
      {#if result.sites.length}
        <div>
          <h4 class="h">Sites in the bundle</h4>
          <div class="row wrap">{#each result.sites as s (s.siteSlug)}<span class="chip"><b>{s.siteSlug}</b> seq {s.fromSeq}–{s.toSeq}</span>{/each}</div>
        </div>
      {/if}
      {#if result.errors.length}
        <div>
          <h4 class="h">Rejected entries</h4>
          <div class="table-wrap">
            <table class="table compact" data-testid="upload-errors">
              <thead><tr><th>Activity</th><th>Reason</th></tr></thead>
              <tbody>{#each result.errors as e (e.activityId)}<tr><td class="mono small">{e.activityId}</td><td class="small text-red">{e.reason}</td></tr>{/each}</tbody>
            </table>
          </div>
        </div>
      {/if}
      <div class="row">
        <Button variant="primary" icon={Upload} onclick={again} data-testid="upload-another">Upload another</Button>
        <Button href="/app/deployments/{id}/activities?via=manual">See manual uploads</Button>
      </div>
    </section>
  {:else}
    <div
      class="drop"
      class:over
      role="button"
      tabindex="0"
      aria-label="Drop an .opmlog file or choose one"
      ondragover={(e) => (e.preventDefault(), (over = true))}
      ondragleave={() => (over = false)}
      {ondrop}
      onclick={() => input?.click()}
      onkeydown={(e) => (e.key === 'Enter' || e.key === ' ') && (e.preventDefault(), input?.click())}
      data-testid="drop-zone"
    >
      <Icon icon={Upload} size={26} />
      <div class="strong">Drop an .opmlog file here</div>
      <div class="muted small">or <span class="lnk">choose a file</span> · at most 24 MiB</div>
      <input bind:this={input} type="file" accept=".opmlog" class="sr-only" onchange={(e) => pick(e.currentTarget.files?.[0])} data-testid="file-input" />
    </div>
    {#if file}
      <div class="card card-pad sel">
        <Icon icon={FileText} size={18} />
        <div class="grow" style="min-width:0"><div class="strong ellipsis">{file.name}</div><div class="small muted">{bytes(file.size)}</div></div>
        <Button size="sm" variant="ghost" icon={X} onclick={again} aria-label="Remove file" />
      </div>
      {#if fileErr}<div class="banner error small"><Icon icon={CircleAlert} size={14} class="icon" />{fileErr}</div>{/if}
    {/if}
    {#if error}<div class="banner error small" role="alert" data-testid="upload-error"><Icon icon={CircleAlert} size={14} class="icon" />{error}</div>{/if}
    <div class="row"><Button variant="primary" icon={Upload} loading={busy} disabled={!file || !!fileErr} onclick={upload} data-testid="upload-submit">Upload</Button></div>
  {/if}
</div>

<style>
  .narrow {
    max-width: 820px;
  }
  .drop {
    border: 2px dashed var(--border-strong);
    border-radius: var(--r-xl);
    background: var(--surface);
    padding: 44px 16px;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 6px;
    color: var(--text-2);
    cursor: pointer;
    text-align: center;
    transition:
      border-color var(--dur-fast),
      background var(--dur-fast);
  }
  .drop.over,
  .drop:hover {
    border-color: var(--accent);
    background: var(--accent-soft);
  }
  .lnk {
    color: var(--accent-text);
  }
  .sel {
    display: flex;
    gap: 10px;
    align-items: center;
    margin: var(--sp-3) 0;
  }
  .banner {
    margin-bottom: var(--sp-3);
  }
  .nums {
    gap: var(--sp-3);
  }
  .n {
    border: 1px solid var(--border);
    border-radius: var(--r-lg);
    padding: 12px 14px;
  }
  .n.bad {
    border-color: color-mix(in srgb, var(--red) 40%, var(--border));
    background: var(--red-soft);
  }
  .v {
    font-size: 24px;
    font-weight: 600;
  }
  .l {
    color: var(--text-2);
    font-size: var(--fs-sm);
  }
  .h {
    margin-bottom: 8px;
  }
</style>
