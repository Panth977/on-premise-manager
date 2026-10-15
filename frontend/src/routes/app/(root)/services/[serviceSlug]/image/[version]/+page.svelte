<script lang="ts">
  import type { Image, ImageEnv, ImageImpactItem, ImageInUseDetails, ImageNameUse, ImageRename, PatchImageRequest, Service, UsageItem, ValidationIssue } from '@opm/shared';
  import { validateImageSpec, imageNameChanges, normalizeImageEnv, parseImageReference, Issues } from '@opm/shared';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { onDestroy, untrack } from 'svelte';
  import { api, errIssues, isApiError } from '$lib/api';
  import { Loader } from '$lib/resource.svelte';
  import { session } from '$lib/session.svelte';
  import { toast } from '$lib/toast.svelte';
  import { catalog } from '$lib/stores/catalog.svelte';
  import { useLeaveGuard } from '$lib/util/guard.svelte';
  import { mergeIssues } from '$lib/util/issues';
  import { bytes, fmtDate, shortSha } from '$lib/util/format';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import HistoryButton from '$lib/audit/HistoryButton.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Field from '$lib/ui/Field.svelte';
  import ErrorState from '$lib/ui/ErrorState.svelte';
  import FormSummary from '$lib/ui/FormSummary.svelte';
  import ConfirmDialog from '$lib/ui/ConfirmDialog.svelte';
  import StatusBadge from '$lib/ui/StatusBadge.svelte';
  import CopyButton from '$lib/ui/CopyButton.svelte';
  import RelTime from '$lib/ui/RelTime.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import ReferencePicker from '$lib/images/ReferencePicker.svelte';
  import ImageSpecEditor, { resolveVolumeRows, type ImageSpecForm } from '$lib/images/ImageSpecEditor.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import { Lock, Link, Trash2, RotateCcw, Undo2, ChevronDown, ChevronRight, Info, CircleAlert, Copy, Pencil, Check } from '$lib/ui/icons';

  const sid = $derived(page.params.serviceSlug!);
  const tag = $derived(page.params.version!);
  const svc = new Loader<Service>(() => api.getService({ serviceSlug: sid }));
  const img = new Loader<Image>(() => api.getImage({ serviceSlug: sid, version: tag }));
  const regs = new Loader(() => (session.can('registries', 'read') ? api.listRegistries({}).then((r) => r.items) : Promise.resolve([])));
  const usage = new Loader<UsageItem[]>(() => api.imageUsage({ serviceSlug: sid, version: tag }).then((r) => r.items));

  // ── the form: every editable field; rows remember their stored name (_orig) so a changed name is a rename
  let form = $state<ImageSpecForm | null>(null);
  let base = $state('');
  const formOf = (i: Image): ImageSpecForm => {
    const c = structuredClone($state.snapshot(i)) as Image;
    return {
      kind: c.kind,
      entrypoint: c.entrypoint,
      command: c.command,
      workdir: c.workdir ?? '',
      user: c.user ?? '',
      stopTimeoutSec: c.stopTimeoutSec,
      ports: c.ports.map((p) => ({ ...p, _orig: p.name })),
      env: c.env.map((e) => ({ ...normalizeImageEnv(e), _orig: e.name })),
      volumes: c.volumes.map((v) => ({ ...v, _orig: v.name })),
      healthcheck: c.healthcheck,
      description: c.description,
    };
  };
  const snap = () => JSON.stringify($state.snapshot(form));
  function resetForm(i: Image) {
    form = formOf(i);
    base = snap();
    server = [];
  }
  $effect(() => {
    void sid;
    void tag;
    untrack(() => {
      svc.load();
      regs.load();
      usage.load();
      img.load().then((i) => i && resetForm(i));
    });
  });
  const dirty = $derived(!!form && snap() !== base);
  useLeaveGuard(() => dirty);

  // poll while importing (the form is not touched)
  let timer: ReturnType<typeof setInterval> | null = null;
  const busy = $derived(!!img.data && (img.data.import.status === 'pending' || img.data.import.status === 'copying'));
  $effect(() => {
    if (busy && !timer) timer = setInterval(() => img.load(true), 3000);
    if (!busy && timer) {
      clearInterval(timer);
      timer = null;
      catalog.invalidate();
    }
  });
  onDestroy(() => timer && clearInterval(timer));

  const canWrite = $derived(session.can('images', 'write'));
  // a container page fix link: ?volume=<image volume>&from=<container page path> (same-app paths only)
  const focusVol = $derived(page.url.searchParams.get('volume'));
  const backTo = $derived.by(() => {
    const f = page.url.searchParams.get('from');
    return f && f.startsWith('/app/') && !f.startsWith('//') ? f : null;
  });
  let layersOpen = $state(false);
  let saving = $state(false);
  let server = $state<ValidationIssue[]>([]);

  // ── body + local validation (the same rules as registration: shared validateImageSpec)
  const strip = <T extends { _orig?: string }>(rows: T[]) => rows.map(({ _orig, ...r }) => (void _orig, r));
  const next = $derived.by(() => {
    if (!form) return null;
    const f = $state.snapshot(form) as ImageSpecForm;
    return {
      kind: f.kind,
      entrypoint: f.entrypoint,
      command: f.command,
      workdir: f.workdir.trim() || null,
      user: f.user.trim() || null,
      stopTimeoutSec: f.stopTimeoutSec,
      ports: strip(f.ports),
      env: strip(f.env).map((e) => {
        const o: ImageEnv = { name: e.name, required: e.required, secret: e.secret, description: e.description, options: e.options ?? null };
        if (e.value !== undefined) o.value = e.value;
        return o;
      }),
      volumes: resolveVolumeRows(f.volumes),
      healthcheck: f.healthcheck,
      description: f.description,
    };
  });
  const renames = $derived.by((): ImageRename[] => {
    if (!form) return [];
    const out: ImageRename[] = [];
    for (const p of form.ports) if (p._orig !== undefined && p._orig !== p.name) out.push({ kind: 'port', from: p._orig, to: p.name });
    for (const e of form.env) if (e._orig !== undefined && e._orig !== e.name) out.push({ kind: 'env', from: e._orig, to: e.name });
    for (const v of form.volumes) if (v._orig !== undefined && v._orig !== v.name) out.push({ kind: 'volume', from: v._orig, to: v.name });
    return out;
  });
  const changes = $derived(img.data && next ? imageNameChanges(img.data, next, renames) : null);
  const local = $derived.by(() => {
    if (!next) return [];
    const is = new Issues('publish');
    validateImageSpec(next as unknown as Record<string, unknown>, is);
    return [...is.list, ...(changes?.issues ?? [])];
  });
  const issues = $derived(mergeIssues(local, server));
  const errorCount = $derived(local.filter((i) => i.severity === 'error').length);

  function patchBody(): PatchImageRequest {
    const i = img.data!;
    const n = next!;
    const b: PatchImageRequest = {};
    const same = (a: unknown, c: unknown) => JSON.stringify(a ?? null) === JSON.stringify(c ?? null);
    if (n.kind !== i.kind) b.kind = n.kind;
    if (!same(n.entrypoint, i.entrypoint)) b.entrypoint = n.entrypoint;
    if (!same(n.command, i.command)) b.command = n.command;
    if (n.workdir !== i.workdir) b.workdir = n.workdir;
    if (n.user !== i.user) b.user = n.user;
    if (n.stopTimeoutSec !== i.stopTimeoutSec) b.stopTimeoutSec = n.stopTimeoutSec;
    if (!same(n.ports, i.ports)) b.ports = n.ports;
    if (!same(n.env, i.env.map((e) => normalizeImageEnv(e)))) b.env = n.env;
    if (!same(n.volumes, i.volumes)) b.volumes = n.volumes;
    if (!same(n.healthcheck, i.healthcheck)) b.healthcheck = n.healthcheck;
    if (n.description !== i.description) b.description = n.description;
    if (renames.length) b.renames = renames;
    return b;
  }

  // ── impact (409 in-use)
  let impactOpen = $state(false);
  let impact = $state<ImageImpactItem[]>([]);
  const impactRemovals = $derived(impact.filter((x) => x.change === 'removed'));
  // §21 #94 deployments are detached from their templates: rename everywhere rewrites templates only
  const impactTplRefs = $derived(impact.reduce((a, x) => a + x.usedBy.filter((u) => u.kind === 'template').length, 0));
  const impactDeps = $derived([...new Map(impact.flatMap((x) => x.usedBy.filter((u) => u.kind === 'deployment')).map((u) => [u.deploymentId, u.label])).entries()]);
  async function save(renameEverywhere = false) {
    if (errorCount) {
      toast.error('Fix the highlighted fields first');
      return;
    }
    saving = true;
    server = [];
    try {
      const body = patchBody();
      if (renameEverywhere) body.renameEverywhere = true;
      const i = await api.patchImage({ serviceSlug: sid, version: tag, body });
      const refs = renameEverywhere ? impactTplRefs : 0;
      const deps = renameEverywhere ? impactDeps.length : 0;
      img.set(i);
      resetForm(i);
      impactOpen = false;
      impact = [];
      catalog.invalidate();
      usage.load(true);
      toast.success('Image saved', renameEverywhere ? `Renamed in ${refs} template reference${refs === 1 ? '' : 's'}.${deps ? ` ${deps} deployment${deps === 1 ? '' : 's'} not changed: they need a Template sync or a manual edit.` : ''}` : 'Published revisions keep their copy until the next publish.');
    } catch (err) {
      if (isApiError(err) && err.code === 'conflict' && (err.details as Partial<ImageInUseDetails>)?.reason === 'in-use') {
        impact = (err.details as unknown as ImageInUseDetails).items;
        impactOpen = true;
        return;
      }
      server = errIssues(err);
      toast.error(err, 'Not saved');
    } finally {
      saving = false;
    }
  }
  const baseOf = (u: ImageNameUse) => (u.kind === 'template' ? `/app/templates/${u.templateId}` : `/app/deployments/${u.deploymentId}`);
  function useHref(u: ImageNameUse) {
    const m = /^edge\.routes\[(.+)\]\.portName$/.exec(u.field);
    if (m) return `${baseOf(u)}/sites/${u.siteSlug}/edge/route?id=${encodeURIComponent(m[1]!)}`;
    return `${baseOf(u)}/sites/${u.siteSlug}/containers/${u.containerSlug}`;
  }

  /** port number / protocol changed on a port while the image is used by containers */
  const usedCount = $derived(usage.data?.length ?? 0);
  function portNote(p: { name: string; _orig?: string }): string | null {
    if (!usedCount || !changes) return null;
    const r = changes.portRetargeted.find((x) => x.name === p.name);
    if (!r) return null;
    return `Used by ${usedCount} container${usedCount === 1 ? '' : 's'}: their port mappings and edge routes reference the name "${p.name}", so they follow it — the container port changes ${r.from} → ${r.to} at the next publish.`;
  }

  // ── retry / delete
  let retrying = $state(false);
  async function retry() {
    retrying = true;
    try {
      const i = await api.retryImport({ serviceSlug: sid, version: tag });
      img.set(i);
      toast.success('Import restarted');
    } catch (err) {
      toast.error(err, 'Could not retry');
    } finally {
      retrying = false;
    }
  }
  let delOpen = $state(false);
  let usedBy = $state<UsageItem[]>([]);
  async function doDelete() {
    try {
      await api.deleteImage({ serviceSlug: sid, version: tag });
    } catch (err) {
      if (isApiError(err) && err.code === 'conflict') {
        usedBy = (err.details.usedBy as UsageItem[]) ?? (await api.imageUsage({ serviceSlug: sid, version: tag })).items;
        toast.push('error', 'The image is in use', err.message);
        return false;
      }
      throw err;
    }
    catalog.invalidate();
    toast.success(`Image ${tag} deleted`);
    goto(`/app/services/${sid}`);
  }
  const regLabel = $derived(img.data?.registryId ? (regs.data?.find((r) => r.registryId === img.data?.registryId)?.label ?? img.data.registryId) : 'public (no credentials)');

  // ── edit source (only while the import failed): reference + registry, then retry
  let srcOpen = $state(false);
  let src = $state<{ reference: string; registryId: string | null; auto: boolean }>({ reference: '', registryId: null, auto: false });
  let srcSaving = $state(false);
  let srcErr = $state<string | null>(null);
  function openSource() {
    const i = img.data!;
    src = { reference: i.reference, registryId: i.registryId, auto: false };
    srcErr = null;
    srcOpen = true;
  }
  async function saveSource(retryAfter: boolean) {
    const i = img.data!;
    const pr = parseImageReference(src.reference);
    if (!pr.ok) {
      srcErr = pr.error;
      return;
    }
    srcSaving = true;
    srcErr = null;
    try {
      const body: PatchImageRequest = {};
      if (src.reference.trim() !== i.reference) body.reference = src.reference.trim();
      if (src.registryId !== i.registryId) body.registryId = src.registryId;
      let n = i;
      if (Object.keys(body).length) n = await api.patchImage({ serviceSlug: sid, version: tag, body });
      if (retryAfter) n = await api.retryImport({ serviceSlug: sid, version: tag });
      img.set(n);
      if (!dirty) resetForm(n);
      catalog.invalidate();
      toast.success(retryAfter ? 'Source saved — import restarted' : 'Source saved');
      srcOpen = false;
    } catch (err) {
      srcErr = errIssues(err)[0]?.message ?? (err instanceof Error ? err.message : String(err));
      toast.error(err, 'Not saved');
    } finally {
      srcSaving = false;
    }
  }
</script>

<svelte:head><title>{sid}:{tag} · OPM</title></svelte:head>

<div class="page">
  <PageHeader title="{sid}:{tag}" iconName={svc.data?.icon} iconType="service" back={{ href: `/app/services/${sid}`, label: svc.data?.label ?? 'Service' }} readOnly={!canWrite}>
    {#snippet meta()}{#if img.data}<StatusBadge kind="import" value={img.data.import.status} />{/if}{#if dirty}<span class="badge amber" data-testid="im-dirty">unsaved</span>{/if}{/snippet}
    {#snippet actions()}
      <Button icon={Link} href="/app/services/{sid}/image/{encodeURIComponent(tag)}/used" data-testid="image-used">{usage.data ? `Used by (${usage.data.length})` : 'Used by'}</Button>
      <HistoryButton target="services/{sid}/images/{tag}" title="History of {sid}:{tag}" />
      {#if canWrite && img.data}
        <Button icon={Copy} href="/app/services/{sid}/register-image?cloneFrom={encodeURIComponent(tag)}" data-testid="clone-image">Clone as new version</Button>
        {#if img.data.import.status === 'failed'}
          <Button icon={Pencil} onclick={openSource} data-testid="edit-source">Edit source</Button>
          <Button icon={RotateCcw} loading={retrying} onclick={retry} data-testid="retry-import">Retry import</Button>
        {/if}
        <Button icon={Trash2} variant="danger-outline" onclick={() => ((usedBy = []), (delOpen = true))} data-testid="delete-image">Delete image</Button>
      {/if}
    {/snippet}
  </PageHeader>

  {#if img.loading && !img.data}
    <div class="card card-pad stack"><span class="skel" style="width:40%"></span><span class="skel" style="width:70%"></span><span class="skel" style="width:55%"></span></div>
  {:else if img.error}
    <ErrorState message={img.error} onretry={() => img.load()} />
  {:else if img.data && form}
    {@const i = img.data}
    <div class="stack loose" data-testid="image-form">
      {#if i.import.status === 'failed'}
        <div class="banner error" data-testid="import-error"><Icon icon={CircleAlert} size={16} class="icon" /><div><b>Import failed.</b> {i.import.error} {#if canWrite}<span class="small">Fix the reference or credentials with <b>Edit source</b>, then retry.</span>{/if}</div></div>
      {:else if busy}
        <div class="banner info"><Icon icon={Info} size={16} class="icon" /><div>Import {i.import.status}… this page refreshes every 3 s.</div></div>
      {/if}

      <section class="card locked" data-testid="image-identity">
        <div class="card-head"><Icon icon={Lock} size={14} /><h3>Image identity</h3><span class="faint small">locked — the fetched image never changes; use <b>Clone as new version</b> to fetch another one</span></div>
        <div class="card-body">
          <dl class="idgrid">
            <div><dt>Version</dt><dd class="mono">{i.version}</dd></div>
            <div class="wide"><dt>Reference</dt><dd><span class="row"><code class="mono small break" data-testid="image-reference">{i.reference}</code><CopyButton text={i.reference} /></span></dd></div>
            <div><dt>Registry</dt><dd>{regLabel}</dd></div>
            <div class="wide"><dt>Digest</dt><dd>{#if i.digest}<span class="row"><code class="mono small break">{i.digest}</code><CopyButton text={i.digest} /></span>{:else}<span class="faint">set by the import</span>{/if}</dd></div>
            <div><dt>Platform</dt><dd class="mono">{i.platform}</dd></div>
            <div><dt>Import</dt><dd><span class="row wrap"><StatusBadge kind="import" value={i.import.status} /><span class="small muted">{i.import.mode} · {bytes(i.import.sizeBytes)}{#if i.import.finishedAt} · <RelTime ts={i.import.finishedAt} />{/if}</span></span></dd></div>
            <div><dt>Created</dt><dd title={i.createdAt}>{fmtDate(i.createdAt)}</dd></div>
          </dl>
          <button type="button" class="lt" onclick={() => (layersOpen = !layersOpen)} aria-expanded={layersOpen} data-testid="layers-toggle">
            <Icon icon={layersOpen ? ChevronDown : ChevronRight} size={14} /> Layers ({i.import.layers.length})
          </button>
          {#if layersOpen}
            {#if !i.import.layers.length}<p class="small faint">No layers yet.</p>{:else}
              <div class="table-wrap"><table class="table compact"><thead><tr><th>#</th><th>Digest</th><th class="num">Size</th><th>Media type</th></tr></thead><tbody>
                {#each i.import.layers as l, k (l.digest)}<tr><td>{k + 1}</td><td class="mono small">{shortSha(l.digest, 16)}<CopyButton text={l.digest} /></td><td class="num small">{bytes(l.sizeBytes)}</td><td class="small muted">{l.mediaType}</td></tr>{/each}
              </tbody></table></div>
            {/if}
          {/if}
        </div>
      </section>

      <div class="banner" data-testid="im-note">
        <Icon icon={Info} size={15} class="icon" />
        <div class="small">
          Everything below is editable. Existing deployments keep their <b>published revisions</b> (with their own copy of this image) until they are published again; drafts and templates pick up new image defaults at the next validate / publish.
          Renaming or removing a port, env or volume that containers use asks first{#if usedCount}: this image is used by <b>{usedCount}</b> container{usedCount === 1 ? '' : 's'}{/if}.
        </div>
      </div>

      <FormSummary issues={server} />

      {#if focusVol && form?.volumes.some((v) => v.name === focusVol)}
        {@const fv = form.volumes.find((v) => v.name === focusVol)!}
        <div class="banner warn" data-testid="im-fix-volume">
          <Icon icon={Info} size={15} class="icon" />
          <div class="small">
            Fixing image volume <code class="mono">{fv.name}</code> at <code class="mono">{fv.mountPath}</code>: set <b>Kind</b> to what the image keeps there — <b>Folder</b>, or <b>File</b> for a single file like <code class="mono">flows.json</code> — then <b>Save</b>. Containers re-check their volume rows at once.
            {#if backTo}<a href={backTo} data-testid="im-fix-back">Back to the container</a>{/if}
          </div>
        </div>
      {/if}

      <ImageSpecEditor bind:form slug={sid} shown={issues} all={issues} start={1} disabled={!canWrite} {portNote} focusVolume={focusVol} />

      {#if changes && (changes.renamed.length || changes.removed.length || changes.added.length)}
        <div class="card card-pad pending" data-testid="im-pending">
          <span class="small strong">Name changes:</span>
          {#each changes.renamed as r (r.kind + r.from)}<span class="badge amber" data-testid="pending-renamed">{r.kind} <code>{r.from}</code> → <code>{r.to}</code></span>{/each}
          {#each changes.removed as r (r.kind + r.name)}<span class="badge red" data-testid="pending-removed">{r.kind} <code>{r.name}</code> removed</span>{/each}
          {#each changes.added as r (r.kind + r.name)}<span class="badge green" data-testid="pending-added">{r.kind} <code>{r.name}</code> added</span>{/each}
        </div>
      {/if}

      {#if canWrite}
        <div class="actionbar">
          <span class="small muted">{#if errorCount}{errorCount} problem(s) to fix{:else if dirty}Unsaved changes{:else}No changes{/if}</span>
          <span class="spacer"></span>
          <Button icon={Undo2} disabled={!dirty || saving} onclick={() => resetForm(i)} data-testid="im-reset">Reset</Button>
          <Button variant="primary" icon={Check} loading={saving} disabled={!dirty || errorCount > 0} onclick={() => save(false)} data-testid="im-save">Save changes</Button>
        </div>
      {/if}
    </div>
  {/if}
</div>

<Modal bind:open={impactOpen} title={impactRemovals.length ? 'In use — cannot remove yet' : 'Renamed names are in use'} subtitle="Templates and deployment drafts reference these names. Deployments are detached from their templates; published revisions are never changed." size="lg" busy={saving} testid="impact-modal">
  <div class="stack">
    {#if impactRemovals.length}
      <div class="banner error small" data-testid="impact-blocked"><Icon icon={CircleAlert} size={15} class="icon" /><div>Removing a name that containers use is blocked. Change those containers first (links below), or keep the {impactRemovals.length === 1 ? 'item' : 'items'}.</div></div>
    {:else}
      <div class="banner info small" data-testid="impact-explain"><Icon icon={Info} size={15} class="icon" /><div><b>Rename in templates</b> rewrites every template reference below in one step (each template gets a new version). <b>Deployments are NOT changed</b> — they are detached from their templates: their drafts keep the old names and need a <b>Template sync</b> (take the template change) or a manual edit. Published revisions keep their own copy.</div></div>
    {/if}
    {#each impact as it (it.kind + it.name)}
      <div class="imp" data-testid="impact-item">
        <div class="row wrap">
          <span class="badge {it.change === 'removed' ? 'red' : 'amber'}">{it.change}</span>
          <span class="small">{it.kind}</span>
          <code class="mono">{it.name}</code>{#if it.to}<span>→</span><code class="mono">{it.to}</code>{/if}
          <span class="small faint">{it.usedBy.length} reference{it.usedBy.length === 1 ? '' : 's'}</span>
        </div>
        {#each [['template', it.change === 'removed' ? 'Templates' : 'Templates — will be rewritten', 'impact-templates'], ['deployment', it.change === 'removed' ? 'Deployment drafts' : 'Deployments — NOT changed (detached from their templates): need a Template sync or a manual edit', 'impact-deployments']] as [kind, title, tid] (kind)}
          {@const uses = it.usedBy.filter((u) => u.kind === kind)}
          {#if uses.length}
            <div class="xs muted ugt" data-testid={tid}>{title}</div>
            <ul class="uses">
              {#each uses as u, k (k)}
                <li data-testid="impact-use">
                  <span class="badge">{u.kind === 'template' ? 'template' : 'draft'}</span>
                  <a href={useHref(u)} target="_blank" rel="noopener">{u.label} · {u.siteSlug} / {u.containerSlug}</a>
                  <code class="mono xs">{u.field}</code>
                  {#if u.kind === 'deployment' && it.change === 'renamed'}<a class="xs" href="/app/deployments/{u.deploymentId}/template-sync">Template sync</a>{/if}
                </li>
              {/each}
            </ul>
          {/if}
        {/each}
      </div>
    {/each}
  </div>
  {#snippet footer()}
    <Button onclick={() => (impactOpen = false)} disabled={saving} data-testid="impact-cancel">Cancel</Button>
    {#if !impactRemovals.length}<Button variant="primary" loading={saving} onclick={() => save(true)} data-testid="impact-rename-everywhere">{impactTplRefs ? 'Rename in templates' : 'Rename (deployments unchanged)'}</Button>{/if}
  {/snippet}
</Modal>

<Modal bind:open={srcOpen} title="Edit source of {sid}:{tag}" subtitle="Only while the import failed: fix the reference or the registry, then retry." size="lg" busy={srcSaving} testid="source-modal">
  <div class="stack">
    {#if srcErr}<div class="banner error small" role="alert">{srcErr}</div>{/if}
    <Field label="Image reference" id="src-ref" required>
      <ReferencePicker bind:value={src.reference} bind:registryId={src.registryId} bind:registryAuto={src.auto} registries={regs.data ?? []} id="src-ref" testid="src-ref" />
    </Field>
  </div>
  {#snippet footer()}
    <Button onclick={() => (srcOpen = false)} disabled={srcSaving}>Cancel</Button>
    <Button onclick={() => saveSource(false)} loading={srcSaving} data-testid="src-save">Save</Button>
    <Button variant="primary" icon={RotateCcw} onclick={() => saveSource(true)} loading={srcSaving} data-testid="src-save-retry">Save and retry import</Button>
  {/snippet}
</Modal>

<ConfirmDialog bind:open={delOpen} title="Delete image {tag}?" message="Only an image that no template, draft, head or open proposal uses can be deleted." confirmLabel="Delete image" danger onconfirm={doDelete}>
  {#snippet body()}
    {#if usedBy.length}
      <div class="banner error small" data-testid="delete-usage"><div><b>Used by:</b><ul class="ul">{#each usedBy as u, k (k)}<li>{u.kind} <a href="/app/{u.kind === 'template' ? 'templates' : 'deployments'}/{u.id}">{u.label}</a> · site {u.siteSlug}</li>{/each}</ul></div></div>
    {/if}
  {/snippet}
</ConfirmDialog>

<style>
  .locked {
    background: var(--surface-2);
  }
  .idgrid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
    gap: 10px 20px;
    margin: 0;
  }
  .idgrid .wide {
    grid-column: span 2;
  }
  @media (max-width: 560px) {
    .idgrid .wide {
      grid-column: auto;
    }
  }
  .idgrid dt {
    font-size: var(--fs-xs);
    color: var(--text-3);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    margin-bottom: 2px;
  }
  .idgrid dd {
    margin: 0;
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .lt {
    display: flex;
    align-items: center;
    gap: 6px;
    border: 0;
    background: transparent;
    padding: 12px 0 0;
    cursor: pointer;
    font-weight: var(--fw-medium);
    color: var(--text);
  }
  .pending {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: center;
  }
  .pending code {
    font-family: var(--font-mono);
  }
  .actionbar {
    position: sticky;
    bottom: 0;
    z-index: var(--z-sticky);
    display: flex;
    gap: 8px;
    align-items: center;
    padding: 12px 16px;
    background: color-mix(in srgb, var(--surface) 92%, transparent);
    backdrop-filter: blur(6px);
    border: 1px solid var(--border);
    border-radius: var(--r-lg);
    box-shadow: var(--shadow-md);
    flex-wrap: wrap;
  }
  .imp {
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    padding: 10px 12px;
  }
  .uses {
    margin: 8px 0 0;
    padding-left: 0;
    list-style: none;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .uses li {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
    font-size: var(--fs-sm);
  }
  .ul {
    margin: 4px 0 0;
    padding-left: 16px;
  }
  .ugt {
    margin-top: 6px;
    font-weight: 600;
  }
</style>
