<script lang="ts">
  import type { HealthCheck, Image, ImageEnv, ImageKind, ImagePort, ImportMode, InspectImageResponse, Platform, RegisterImageRequest, Service, ValidationIssue } from '@opm/shared';
  import { validateRegisterImage, nextVersion, normalizeImageEnv, parseImageReference, inferImageVolumeKind } from '@opm/shared';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { onMount, tick } from 'svelte';
  import { api, errIssues, isApiError } from '$lib/api';
  import { Loader } from '$lib/resource.svelte';
  import { session } from '$lib/session.svelte';
  import { toast } from '$lib/toast.svelte';
  import { catalog } from '$lib/stores/catalog.svelte';
  import { useLeaveGuard } from '$lib/util/guard.svelte';
  import { errorsAt, mergeIssues, issue } from '$lib/util/issues';
  import { bytes, shortSha, relTime } from '$lib/util/format';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Field from '$lib/ui/Field.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import ErrorState from '$lib/ui/ErrorState.svelte';
  import FormSummary from '$lib/ui/FormSummary.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import Combobox, { type ComboOption } from '$lib/ui/Combobox.svelte';
  import StatusBadge from '$lib/ui/StatusBadge.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import ImageSpecEditor, { resolveVolumeRows, type VolumeRow } from '$lib/images/ImageSpecEditor.svelte';
  import ReferencePicker from '$lib/images/ReferencePicker.svelte';
  import { Download, Info, Package, Copy } from '$lib/ui/icons';

  const sid = $derived(page.params.serviceSlug!);
  const svc = new Loader<Service>(() => api.getService({ serviceSlug: sid }));
  const regs = new Loader(() => api.listRegistries({}).then((r) => r.items));
  const imgs = new Loader<Image[]>(() => api.listImages({ serviceSlug: sid }).then((r) => [...r.items].sort((a, b) => b.createdAt.localeCompare(a.createdAt))));

  interface Form {
    version: string;
    reference: string;
    registryId: string | null;
    platform: Platform;
    mode: ImportMode;
    kind: ImageKind;
    entrypoint: string[] | null;
    command: string[] | null;
    workdir: string;
    user: string;
    stopTimeoutSec: number;
    ports: ImagePort[];
    env: ImageEnv[];
    volumes: VolumeRow[];
    healthcheck: HealthCheck | null;
    description: string;
  }
  const empty = (): Form => ({ version: '', reference: '', registryId: null, platform: 'linux/amd64', mode: 'copy', kind: 'service', entrypoint: null, command: null, workdir: '', user: '', stopTimeoutSec: 30, ports: [], env: [], volumes: [], healthcheck: null, description: '' });
  let form = $state<Form>(empty());
  let registryAuto = $state(true);
  const snap = () => JSON.stringify($state.snapshot(form));
  let initial = $state(snap());
  let done = false;
  const dirty = $derived(snap() !== initial);
  useLeaveGuard(() => dirty && !done);

  const taken = $derived((imgs.data ?? []).map((i) => i.version));
  const suggested = $derived(nextVersion(imgs.data?.[0]?.version ?? null, taken));

  onMount(async () => {
    svc.load();
    regs.load();
    const list = await imgs.load();
    form.version = nextVersion(list?.[0]?.version ?? null, (list ?? []).map((i) => i.version));
    const cf = page.url.searchParams.get('cloneFrom');
    if (list?.length) cloneFrom = cf && list.some((i) => i.version === cf) ? cf : list[0]!.version;
    initial = snap();
    if (cf && list?.some((i) => i.version === cf)) applyClone();
  });

  // ── clone from a past version
  let cloneFrom = $state('');
  let cloned = $state<Image | null>(null);
  let refPicker = $state<{ openList: () => void } | null>(null);
  let refInput = $state<HTMLInputElement | null>(null);
  const regLabel = (id: string | null) => (id ? (regs.data?.find((r) => r.registryId === id)?.label ?? id) : 'public');
  const cloneOptions = $derived<ComboOption[]>(
    (imgs.data ?? []).map((i) => ({ value: i.version, label: i.version, sub: `${i.reference} · ${regLabel(i.registryId)}`, badge: i.import.status, detail: relTime(i.createdAt) })),
  );
  async function applyClone() {
    const src = imgs.data?.find((i) => i.version === cloneFrom);
    if (!src) return;
    const c = structuredClone($state.snapshot(src)) as Image;
    form = {
      version: nextVersion(imgs.data?.[0]?.version ?? null, taken),
      reference: c.reference,
      registryId: c.registryId,
      platform: c.platform,
      mode: c.import.mode,
      kind: c.kind,
      entrypoint: c.entrypoint,
      command: c.command,
      workdir: c.workdir ?? '',
      user: c.user ?? '',
      stopTimeoutSec: c.stopTimeoutSec,
      ports: c.ports,
      env: c.env.map((e) => normalizeImageEnv(e)),
      volumes: c.volumes,
      healthcheck: c.healthcheck,
      description: c.description,
    };
    registryAuto = false;
    cloned = src;
    inspected = null;
    toast.success(`Cloned from ${src.version}`, 'Change the tag in the image reference, then register.');
    await tick();
    // select the TAG part of the reference so typing replaces it; the list shows the tags of that repository
    const ref = form.reference;
    const at = ref.indexOf('@');
    const body = at >= 0 ? ref.slice(0, at) : ref;
    const colon = body.lastIndexOf(':');
    const slash = body.lastIndexOf('/');
    if (refInput && colon > slash) {
      refInput.focus();
      refInput.setSelectionRange(colon + 1, body.length);
    }
    refPicker?.openList();
  }
  const clonedTag = $derived(cloned ? (cloned.source.tag ?? shortSha(cloned.source.digest)) : null);

  const body = $derived.by((): RegisterImageRequest => {
    const f = $state.snapshot(form) as Form;
    return {
      version: f.version.trim(),
      reference: f.reference.trim(),
      registryId: f.registryId,
      platform: f.platform,
      mode: f.mode,
      kind: f.kind,
      entrypoint: f.entrypoint,
      command: f.command,
      workdir: f.workdir.trim() || null,
      user: f.user.trim() || null,
      ports: f.ports,
      env: f.env.map((e) => {
        const o: ImageEnv = { name: e.name, required: e.required, secret: e.secret, description: e.description, options: e.options ?? null };
        if (e.value !== undefined) o.value = e.value;
        return o;
      }),
      volumes: resolveVolumeRows(f.volumes),
      healthcheck: f.healthcheck,
      stopTimeoutSec: f.stopTimeoutSec,
      description: f.description,
    };
  });
  let touched = $state(false);
  let server = $state<ValidationIssue[]>([]);
  const localRaw = $derived(validateRegisterImage(body as unknown as Record<string, unknown>).list);
  /** friendlier messages for the common mount-path mistake */
  const local = $derived(
    localRaw.map((i) => {
      const m = /^volumes\[(.+)\]\.mountPath$/.exec(i.path);
      if (m && (i.code === 'V_PATH' || i.code === 'V_MOUNT')) {
        const v = form.volumes.find((x) => x.name === m[1]);
        if (v && v.mountPath && !v.mountPath.startsWith('/')) return { ...i, message: 'must start with / — relative paths are invalid' };
      }
      return i;
    }),
  );
  const issues = $derived(mergeIssues(local, server));
  const shownIssues = $derived(touched || server.length ? issues : []);
  const e = (...p: string[]) => errorsAt(shownIssues, ...p);
  const refParsed = $derived(form.reference.trim() ? parseImageReference(form.reference) : null);

  const slug = $derived(sid);

  // ── read from registry (+ diff when the form already has values)
  let reading = $state(false);
  let inspected = $state<InspectImageResponse | null>(null);
  let diffOpen = $state(false);
  let pending = $state<InspectImageResponse | null>(null);
  const filled = $derived(form.ports.length > 0 || form.env.length > 0 || form.volumes.length > 0 || !!form.entrypoint || !!form.command || !!form.workdir || !!form.user);
  type DiffRow = { area: string; name: string; kind: 'added' | 'removed' | 'changed'; before?: string; after?: string };
  const diffRows = $derived.by((): DiffRow[] => {
    const r = pending;
    if (!r) return [];
    const out: DiffRow[] = [];
    const js = (v: unknown) => (v === null || v === undefined ? '—' : typeof v === 'string' ? v : JSON.stringify(v));
    for (const k of ['entrypoint', 'command', 'workdir', 'user'] as const) {
      const before = k === 'workdir' || k === 'user' ? form[k] || null : form[k];
      const after = r[k];
      if (JSON.stringify(before ?? null) !== JSON.stringify(after ?? null)) out.push({ area: 'runtime', name: k, kind: 'changed', before: js(before), after: js(after) });
    }
    const cmp = <T extends { name: string }>(area: string, a: T[], b: T[], show: (x: T) => string) => {
      for (const x of b) {
        const y = a.find((z) => z.name === x.name);
        if (!y) out.push({ area, name: x.name, kind: 'added', after: show(x) });
        else if (show(x) !== show(y)) out.push({ area, name: x.name, kind: 'changed', before: show(y), after: show(x) });
      }
      for (const y of a) if (!b.some((x) => x.name === y.name)) out.push({ area, name: y.name, kind: 'removed', before: show(y) });
    };
    cmp('ports', form.ports, r.ports, (p) => `${p.port}/${p.protocol}`);
    cmp('env', form.env, r.env, (v) => (v.value === undefined ? '(no default)' : v.value));
    const kindOf = (v: { mountPath: string; kind: string }) => (v.kind === 'auto' ? inferImageVolumeKind(v.mountPath) : v.kind);
    cmp('volumes', form.volumes as { name: string; mountPath: string; kind: string }[], r.volumes, (v) => `${v.mountPath} (${kindOf(v) === 'file' ? 'File' : 'Folder'})`);
    return out;
  });
  async function readRegistry() {
    if (!refParsed?.ok) {
      touched = true;
      toast.warning('Enter a valid image reference first');
      return;
    }
    reading = true;
    try {
      const r = await api.inspectImage({ serviceSlug: sid, body: { reference: form.reference.trim(), registryId: form.registryId, platform: form.platform } });
      if (filled) {
        pending = r;
        if (diffRows.length) diffOpen = true;
        else {
          inspected = r;
          pending = null;
          toast.success('The registry agrees with the form', `${r.layers} layers · ${bytes(r.sizeBytes)} · ${shortSha(r.digest)}`);
        }
      } else apply(r);
    } catch (err) {
      toast.error(err, 'Could not read the image');
    } finally {
      reading = false;
    }
  }
  /**
   * Apply what the registry reports. Kept from the form: env rows that are a choice (options) or hold an expression,
   * volumes whose mount path is an expression, and every env/volume description, required and secret flag.
   */
  function apply(r: InspectImageResponse) {
    inspected = r;
    const prevEnv = $state.snapshot(form.env) as ImageEnv[];
    const prevVol = $state.snapshot(form.volumes) as VolumeRow[];
    form.entrypoint = r.entrypoint;
    form.command = r.command;
    form.workdir = r.workdir ?? '';
    form.user = r.user ?? '';
    form.ports = structuredClone($state.snapshot(r.ports));
    form.env = r.env.map((x) => {
      const p = prevEnv.find((y) => y.name === x.name);
      if (!p) return normalizeImageEnv(structuredClone(x));
      if (Array.isArray(p.options) || (p.value ?? '').includes('{{')) return p;
      return { ...p, ...(x.value === undefined ? {} : { value: x.value }) };
    });
    form.volumes = r.volumes.map((v) => {
      const p = prevVol.find((y) => y.name === v.name);
      return p && p.mountPath.includes('{{') ? p : { ...v, description: p?.description ?? v.description };
    });
    if (form.healthcheck && (form.healthcheck.type === 'http' || form.healthcheck.type === 'tcp') && !r.ports.some((p) => p.name === (form.healthcheck as { portName: string }).portName)) form.healthcheck = null;
    pending = null;
    diffOpen = false;
    toast.success('Filled from the registry', `${r.layers} layers · ${bytes(r.sizeBytes)} · ${shortSha(r.digest)}`);
  }
  function keepClone() {
    inspected = pending;
    pending = null;
    diffOpen = false;
  }

  // ── submit
  let saving = $state(false);
  async function submit(ev: Event) {
    ev.preventDefault();
    touched = true;
    server = [];
    if (local.some((i) => i.severity === 'error')) {
      toast.error('Fix the highlighted fields first');
      return;
    }
    saving = true;
    try {
      const img = await api.registerImage({ serviceSlug: sid, body });
      done = true;
      catalog.invalidate();
      toast.success(`Version ${img.version} registered`, 'The import runs in the background.');
      goto(`/app/services/${sid}/image/${encodeURIComponent(img.version)}`);
    } catch (err) {
      if (isApiError(err) && err.code === 'conflict') server = [issue('version', 'V_DUPLICATE', `Version ${body.version} already exists — use ${suggested}`)];
      else server = errIssues(err);
      toast.error(err, 'Not registered');
    } finally {
      saving = false;
    }
  }
  const canWrite = $derived(session.can('images', 'write'));
</script>

<svelte:head><title>Register image · OPM</title></svelte:head>

<div class="page">
  <PageHeader title="Register image" iconName={svc.data?.icon} iconType="service" back={{ href: `/app/services/${sid}`, label: svc.data?.label ?? 'Service' }} subtitle={svc.data ? `A new version of ${svc.data.label} (${sid}) from a docker image reference` : undefined} />

  {#if svc.error}
    <ErrorState message={svc.error} onretry={() => svc.load()} />
  {:else if !canWrite}
    <ErrorState title="Not allowed" message="You need images.write to register images." />
  {:else}
    <form class="stack loose" onsubmit={submit} novalidate data-testid="register-form">
      {#if imgs.data?.length}
        <section class="card clone" data-testid="clone-card">
          <div class="card-body">
            <div class="row wrap cl">
              <Icon icon={Copy} size={16} />
              <span class="strong">Clone from…</span>
              <div class="cpick"><Combobox bind:value={cloneFrom} options={cloneOptions} placeholder="Pick a version" ariaLabel="Version to clone" testid="clone-from" /></div>
              <Button onclick={applyClone} disabled={!cloneFrom} data-testid="clone-apply">Clone</Button>
              <span class="small muted grow">Copies everything (env, volumes, health check …); you change the tag of the image reference. Nothing is applied until you click Clone.</span>
            </div>
            {#if cloned}<div class="small cloned" data-testid="clone-applied">Cloned from <b>{cloned.version}</b> — the reference was <code class="mono">{cloned.reference}</code>. Pick the new tag (was <b>{clonedTag}</b>).</div>{/if}
          </div>
        </section>
      {/if}

      <FormSummary issues={shownIssues} />

      <section class="card">
        <div class="card-head"><span class="num">1</span><h3>Source</h3></div>
        <div class="card-body stack">
          <div class="grid-src">
            <Field label="Version" id="ri-version" required hint="OPM version of this service (v1, v2, 3.1.0) · fixed · sites tag the image {sid}:{form.version || suggested}" error={e('version')}>
              <div class="row ver">
                <input id="ri-version" class="input mono" bind:value={form.version} placeholder={suggested} maxlength="64" data-testid="ri-version" />
                {#if form.version !== suggested && !taken.includes(suggested)}<button type="button" class="chip sug" onclick={() => (form.version = suggested)} data-testid="ri-version-suggest">use {suggested}</button>{/if}
              </div>
            </Field>
            <Field label="Image reference" id="ri-ref" required hint={clonedTag ? `Cloned — was ${clonedTag}; pick or type the new tag` : 'Paste a full reference, or pick host → repository → tag'} error={e('reference', 'registryId')}>
              <ReferencePicker bind:this={refPicker} bind:value={form.reference} bind:registryId={form.registryId} bind:registryAuto bind:inputEl={refInput} registries={regs.data ?? []} registered={imgs.data ?? []} id="ri-ref" invalid={e('reference').length > 0} />
            </Field>
          </div>
          <div class="grid-2">
            <Field label="Platform" id="ri-plat" error={e('platform')}>
              <SegmentedControl options={['linux/amd64', 'linux/arm64'] as Platform[]} bind:value={form.platform} ariaLabel="Platform" testid="ri-platform" />
            </Field>
            <Field label="Mode" id="ri-mode" hint={form.mode === 'copy' ? 'The cloud copies every layer into its storage now.' : 'The cloud fetches layers from the registry when a site first asks.'} error={e('mode')}>
              <SegmentedControl options={['copy', 'gateway'] as ImportMode[]} bind:value={form.mode} ariaLabel="Import mode" testid="ri-mode" />
            </Field>
          </div>
          <div class="row wrap">
            <Button icon={Download} loading={reading} onclick={readRegistry} data-testid="ri-read">Read from registry</Button>
            {#if inspected}<span class="small muted">Read {shortSha(inspected.digest)} · {inspected.layers} layers · {bytes(inspected.sizeBytes)} · {inspected.platform}</span>{:else}<span class="small faint">Fills runtime, ports, env and volumes from the image config{cloned ? ' (shows what differs from the clone first)' : ''}.</span>{/if}
          </div>
        </div>
      </section>

      <ImageSpecEditor bind:form {slug} shown={shownIssues} all={issues} start={2} />

      <div class="actionbar">
        <span class="small muted">{#if touched && issues.some((i) => i.severity === 'error')}{issues.filter((i) => i.severity === 'error').length} problem(s) to fix{:else}Registering starts the import.{/if}</span>
        <span class="spacer"></span>
        <Button href="/app/services/{sid}">Cancel</Button>
        <Button type="submit" variant="primary" icon={Package} loading={saving} disabled={touched && local.some((i) => i.severity === 'error')} data-testid="ri-submit">Register and import</Button>
      </div>
    </form>
  {/if}
</div>

<Modal bind:open={diffOpen} title="The registry reports something else" subtitle={cloned ? `Differences between the clone of ${cloned.version} and ${form.reference}` : 'Differences between the form and the image config'} size="lg" testid="ri-diff">
  <div class="banner info small" style="margin-bottom:10px"><Icon icon={Info} size={15} class="icon" /><div>Apply all keeps choice env defaults, expressions and descriptions from the form.</div></div>
  <div class="table-wrap">
    <table class="table compact">
      <thead><tr><th>Area</th><th>Name</th><th>Change</th><th>Form</th><th>Registry</th></tr></thead>
      <tbody>
        {#each diffRows as d, k (k)}
          <tr data-testid="diff-row">
            <td class="small">{d.area}</td>
            <td class="mono small">{d.name}</td>
            <td><span class="badge {d.kind === 'added' ? 'green' : d.kind === 'removed' ? 'red' : 'amber'}">{d.kind}</span></td>
            <td class="mono small break">{d.before ?? ''}</td>
            <td class="mono small break">{d.after ?? ''}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
  {#snippet footer()}
    <Button onclick={keepClone} data-testid="diff-keep">{cloned ? 'Keep the clone' : 'Keep the form'}</Button>
    <Button variant="primary" onclick={() => pending && apply(pending)} data-testid="diff-apply">Apply all</Button>
  {/snippet}
</Modal>

<style>
  .card-head .num {
    width: 22px;
    height: 22px;
    border-radius: 50%;
    background: var(--accent-soft);
    color: var(--accent-text);
    font-size: 12px;
    font-weight: 600;
    display: grid;
    place-items: center;
  }
  .clone {
    border-style: dashed;
  }
  .cl {
    gap: 10px;
    align-items: center;
  }
  .cpick {
    width: 300px;
    max-width: 100%;
  }
  .cloned {
    margin-top: 8px;
    color: var(--text-2);
  }
  .grid-src {
    display: grid;
    grid-template-columns: minmax(180px, 240px) minmax(0, 1fr);
    gap: var(--sp-4);
  }
  @media (max-width: 720px) {
    .grid-src {
      grid-template-columns: minmax(0, 1fr);
    }
  }
  .ver {
    gap: 6px;
    flex-wrap: nowrap;
  }
  .sug {
    cursor: pointer;
    white-space: nowrap;
    background: var(--accent-soft);
    color: var(--accent-text);
    border-color: transparent;
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
</style>
