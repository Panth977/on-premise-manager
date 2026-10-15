<!--
  The editable image spec (SPEC §21 #82): runtime (kind, entrypoint, command, workdir, user, stop timeout), ports, env,
  volumes, health check, description. ONE editor for the register-image page and the image page, so both have the same
  controls and validation (shared validateImageSpec). Rows may carry `_orig` (the stored name, image page): a changed
  name shows "renamed from …" and becomes a rename in the PATCH body. Volume rows have a kind control (§21 #83):
  Auto (new rows: follows inferImageVolumeKind(mount path)) · Folder · File; `resolveVolumeRows` stores the explicit kind.
-->
<script lang="ts" module>
  import type { ExprContext, HealthCheck, ImageEnv, ImageKind, ImagePort, ImageVolume, ImageVolumeKind } from '@opm/shared';
  import { inferImageVolumeKind } from '@opm/shared';
  export type Orig = { _orig?: string };
  /** 'auto' = follow the mount path (inferImageVolumeKind) until the user picks Folder / File */
  export type VolumeKindChoice = ImageVolumeKind | 'auto';
  export type VolumeRow = Omit<ImageVolume, 'kind'> & { kind: VolumeKindChoice } & Orig;
  /** The volumes as stored: explicit kind (Auto → inferred from the mount path), no _orig; key order of normalizeImageVolume. */
  export function resolveVolumeRows(rows: VolumeRow[]): ImageVolume[] {
    return rows.map((v) => ({ name: v.name, mountPath: v.mountPath, kind: v.kind === 'auto' ? inferImageVolumeKind(v.mountPath) : v.kind, description: v.description }));
  }
  export interface ImageSpecForm {
    kind: ImageKind;
    entrypoint: string[] | null;
    command: string[] | null;
    workdir: string;
    user: string;
    stopTimeoutSec: number;
    ports: (ImagePort & Orig)[];
    env: (ImageEnv & Orig)[];
    volumes: VolumeRow[];
    healthcheck: HealthCheck | null;
    description: string;
  }
  /** expression context for mount paths / env defaults: container.env.<NAME> from the env rows of the form */
  export function specExprCtx(slug: string, env: ImageEnv[]): ExprContext {
    return {
      deployment: { id: 'DeploymentId00000001' },
      self: { slug: 'site', host: 'site.example' },
      var: {},
      container: { slug, env: Object.fromEntries(env.filter((x) => x.name).map((x) => [x.name, { value: x.secret ? null : (x.value ?? null), secret: x.secret }])) },
    };
  }
</script>

<script lang="ts">
  import type { ValidationIssue } from '@opm/shared';
  import Field from '$lib/ui/Field.svelte';
  import StringListEditor from '$lib/ui/StringListEditor.svelte';
  import NumberExprInput from '$lib/ui/NumberExprInput.svelte';
  import NumberStepper from '$lib/ui/NumberStepper.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import ExprInput from '$lib/ui/ExprInput.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import HealthCheckEditor from '$lib/images/HealthCheckEditor.svelte';
  import EnvDefaultEditor from '$lib/images/EnvDefaultEditor.svelte';
  import { errorsAt } from '$lib/util/issues';
  import { Plus, X, CircleHelp, TriangleAlert } from '$lib/ui/icons';
  import { VOLUME_KIND_LABEL } from '@opm/shared';
  import type { Snippet } from 'svelte';

  type Props = {
    form: ImageSpecForm;
    /** container slug used in the expression preview */
    slug: string;
    /** issues shown after the first submit (register) or always (image page) */
    shown: ValidationIssue[];
    /** issues shown right away inline (env / volume rows) */
    all: ValidationIssue[];
    /** number of the first section card */
    start?: number;
    disabled?: boolean;
    /** extra note under a port row (image page: in-use port retarget warning) */
    portNote?: (p: ImagePort & Orig) => string | null;
    /** extra content in the header of a section (image page: "used by …" hints) */
    headExtra?: Snippet<[string]>;
    /** image volume to scroll to and highlight (image page `?volume=<name>`, from a container page fix link) */
    focusVolume?: string | null;
  };
  let { form = $bindable(), slug, shown, all, start = 2, disabled = false, portNote, headExtra, focusVolume = null }: Props = $props();

  const e = (...p: string[]) => errorsAt(shown, ...p);
  const eNow = (...p: string[]) => errorsAt(all, ...p);
  const portNames = $derived(form.ports.map((p) => p.name).filter(Boolean));
  const exprCtx = $derived(specExprCtx(slug, form.env));
  const renamedFrom = (r: Orig & { name: string }) => (r._orig !== undefined && r._orig !== r.name ? r._orig : null);

  /** typing a port name keeps the image's own health check on that port */
  function renamePort(p: ImagePort, name: string) {
    const hc = form.healthcheck;
    if (hc && (hc.type === 'http' || hc.type === 'tcp') && hc.portName === p.name && p.name && !form.ports.some((x) => x !== p && x.name === p.name)) hc.portName = name;
    p.name = name;
  }
  const addPort = () => form.ports.push({ name: '', port: '' as unknown as number, protocol: 'tcp', description: '' });
  const addEnv = () => form.env.push({ name: '', required: false, secret: false, description: '', options: null });
  const addVol = () => form.volumes.push({ name: '', mountPath: '', kind: 'auto', description: '' });
  /** Folder / File; a new row also offers Auto (follows the path) until Folder or File is picked */
  const kindOptions = (v: VolumeRow) => [
    ...(v.kind === 'auto' ? [{ value: 'auto' as const, label: `Auto · ${VOLUME_KIND_LABEL[inferImageVolumeKind(v.mountPath)]}`, title: 'From the mount path: a last segment with an extension (flows.json, settings.js) is a file, anything else a folder. Saved as Folder / File.' }] : []),
    { value: 'dir' as const, label: 'Folder', title: 'The image keeps a folder at this path' },
    { value: 'file' as const, label: 'File', title: 'The image keeps ONE file at this path (e.g. /data/flows.json)' },
  ];
  // a container page fix link (?volume=<name>) scrolls to that row once
  let scrolled = false;
  $effect(() => {
    if (!focusVolume || scrolled || !form.volumes.some((v) => v.name === focusVolume)) return;
    scrolled = true;
    queueMicrotask(() => document.querySelector(`[data-vol-row="${CSS.escape(focusVolume!)}"]`)?.scrollIntoView({ block: 'center' }));
  });
  const MOUNT_HELP = 'Expressions read values of this container: /app/helper/config/env/{{ container.env.NODE_ENV }}.ts mounts the file named after the final NODE_ENV (image default or the container value). Must start with "/".';
</script>

<fieldset class="spec" {disabled}>
  <section class="card">
    <div class="card-head"><span class="num">{start}</span><h3>Runtime</h3>{@render headExtra?.('runtime')}</div>
    <div class="card-body stack">
      <Field label="Kind" id="ri-kind" hint={form.kind === 'job' ? 'Runs to completion (exit 0); restart never / on-failure.' : 'Long-running; ready when its health check passes.'} error={e('kind')}>
        <SegmentedControl options={['service', 'job'] as ImageKind[]} bind:value={form.kind} ariaLabel="Kind" testid="ri-kind" {disabled} />
      </Field>
      <div class="grid-2">
        <Field label="Entrypoint" id="ri-ep" hint="Empty = image default" error={e('entrypoint')}><StringListEditor bind:value={form.entrypoint} placeholder="(image default)" addLabel="Add argument" /></Field>
        <Field label="Command" id="ri-cmd" hint="Empty = image default; containers may override" error={e('command')}><StringListEditor bind:value={form.command} placeholder="(image default)" addLabel="Add argument" /></Field>
      </div>
      <div class="grid-3">
        <Field label="Workdir" id="ri-wd" hint="Absolute path or empty" error={e('workdir')}><input id="ri-wd" class="input mono" bind:value={form.workdir} placeholder="/app" data-testid="ri-workdir" /></Field>
        <Field label="User" id="ri-user" hint="name or uid:gid, or empty" error={e('user')}><input id="ri-user" class="input mono" bind:value={form.user} placeholder="65532:65532" data-testid="ri-user" /></Field>
        <Field label="Stop timeout (s)" id="ri-stop" hint="1–600" error={e('stopTimeoutSec')}><NumberStepper id="ri-stop" bind:value={form.stopTimeoutSec} min={1} max={600} step={5} testid="ri-stop" {disabled} /></Field>
      </div>
    </div>
  </section>

  <section class="card">
    <div class="card-head"><span class="num">{start + 1}</span><h3>Ports</h3>{@render headExtra?.('ports')}<span class="spacer"></span>{#if !disabled}<Button size="sm" icon={Plus} onclick={addPort} data-testid="ri-add-port">Add port</Button>{/if}</div>
    <div class="card-body">
      {#if !form.ports.length}<p class="small faint">No ports. Add the ports the container listens on (the edge can route HTTP to them).</p>{:else}
        <div class="rows">
          <div class="rh ports"><span>Name</span><span>Port</span><span>Protocol</span><span>Description</span><span></span></div>
          {#each form.ports as p, i (i)}
            {@const at = `ports[${p.name}]`}
            {@const note = portNote?.(p) ?? null}
            <div class="rr ports" data-testid="ri-port-row">
              <Field error={e(`${at}.name`)}>
                <input class="input sm mono" value={p.name} oninput={(ev) => renamePort(p, ev.currentTarget.value)} placeholder="http" aria-label="Port name" data-testid="port-name" />
                {#if renamedFrom(p)}<span class="ren" data-testid="port-renamed">renamed from <code>{renamedFrom(p)}</code></span>{/if}
              </Field>
              <Field error={e(`${at}.port`)}><NumberExprInput value={p.port} onchange={(v) => (p.port = v as number)} field="imagePort" nullable={false} size="sm" placeholder="8080" ariaLabel="Port number" /></Field>
              <SegmentedControl options={['tcp', 'udp'] as ('tcp' | 'udp')[]} bind:value={p.protocol} size="sm" ariaLabel="Protocol" {disabled} />
              <input class="input sm" bind:value={p.description} placeholder="HTTP" aria-label="Port description" data-testid="port-desc" />
              {#if !disabled}<button type="button" class="btn ghost icon sm" aria-label="Remove port" onclick={() => form.ports.splice(i, 1)} data-testid="port-remove"><Icon icon={X} size={14} /></button>{:else}<span></span>{/if}
            </div>
            {#if note}<div class="rnote" data-testid="port-note"><Icon icon={TriangleAlert} size={13} /> {note}</div>{/if}
          {/each}
        </div>
      {/if}
    </div>
  </section>

  <section class="card">
    <div class="card-head"><span class="num">{start + 2}</span><h3>Env</h3>{@render headExtra?.('env')}<span class="spacer"></span>{#if !disabled}<Button size="sm" icon={Plus} onclick={addEnv} data-testid="ri-add-env">Add env</Button>{/if}</div>
    <div class="card-body">
      {#if !form.env.length}<p class="small faint">No env. Declare the variables the image reads; containers set values.</p>{:else}
        <div class="rows">
          <div class="rh env"><span>Name</span><span>Default</span><span>Required</span><span>Secret</span><span>Description</span><span></span></div>
          {#each form.env as v, i (i)}
            {@const at = `env[${v.name}]`}
            <div class="rr env" data-testid="ri-env-row">
              <Field error={eNow(`${at}.name`).length && v.name ? eNow(`${at}.name`) : e(`${at}.name`)}>
                <input class="input sm mono" bind:value={v.name} placeholder="LOG_LEVEL" aria-label="Env name" oninput={(ev) => (v.name = ev.currentTarget.value.toUpperCase())} data-testid="env-name" />
                {#if renamedFrom(v)}<span class="ren" data-testid="env-renamed">renamed from <code>{renamedFrom(v)}</code></span>{/if}
              </Field>
              <Field error={eNow(`${at}.value`, `${at}.options`)}><EnvDefaultEditor bind:value={v.value} bind:options={v.options} disabled={v.secret || disabled} ctx={exprCtx} invalid={eNow(`${at}.value`).length > 0} /></Field>
              <label class="check"><input type="checkbox" bind:checked={v.required} data-testid="env-required" />req.</label>
              <label class="check"><input type="checkbox" bind:checked={v.secret} onchange={() => v.secret && (delete v.value, (v.options = null))} data-testid="env-secret" />secret</label>
              <input class="input sm" bind:value={v.description} aria-label="Env description" data-testid="env-desc" />
              {#if !disabled}<button type="button" class="btn ghost icon sm" aria-label="Remove env" onclick={() => form.env.splice(i, 1)} data-testid="env-remove"><Icon icon={X} size={14} /></button>{:else}<span></span>{/if}
            </div>
          {/each}
        </div>
      {/if}
    </div>
  </section>

  <section class="card">
    <div class="card-head">
      <span class="num">{start + 3}</span><h3>Volumes</h3>
      <span class="help" data-tip={MOUNT_HELP} data-testid="mount-help" aria-label="How to use expressions in a mount path" role="note"><Icon icon={CircleHelp} size={14} /> expressions</span>
      {@render headExtra?.('volumes')}
      <span class="spacer"></span>{#if !disabled}<Button size="sm" icon={Plus} onclick={addVol} data-testid="ri-add-vol">Add volume</Button>{/if}
    </div>
    <div class="card-body">
      {#if !form.volumes.length}<p class="small faint">No volumes. Containers pick a declared site volume, a file, a secret or "Not mounted" per image volume.</p>{:else}
        <div class="rows">
          <div class="rh vol"><span>Name</span><span>Mount path</span><span>Kind</span><span>Description</span><span></span></div>
          {#each form.volumes as v, i (i)}
            {@const at = `volumes[${v.name}]`}
            <div class="rr vol" class:focus={!!focusVolume && v.name === focusVolume} data-vol-row={v.name} data-testid="ri-vol-row">
              <Field error={e(`${at}.name`)}>
                <input class="input sm mono" bind:value={v.name} placeholder="data" aria-label="Volume name" data-testid="vol-name" />
                {#if renamedFrom(v)}<span class="ren" data-testid="vol-renamed">renamed from <code>{renamedFrom(v)}</code></span>{/if}
              </Field>
              <Field error={v.mountPath ? eNow(`${at}.mountPath`) : e(`${at}.mountPath`)}>
                <ExprInput bind:value={v.mountPath} field="mountPath" ctx={exprCtx} size="sm" placeholder={'/app/helper/config/env/{{ container.env.NODE_ENV }}.ts'} ariaLabel="Mount path" invalid={!!v.mountPath && eNow(`${at}.mountPath`).length > 0} testid="vol-mount" />
              </Field>
              <Field error={eNow(`${at}.kind`)}>
                <SegmentedControl options={kindOptions(v)} bind:value={v.kind} size="sm" ariaLabel="Kind of {v.name || 'volume'}" testid="vol-kind" {disabled} />
              </Field>
              <input class="input sm" bind:value={v.description} aria-label="Volume description" data-testid="vol-desc" />
              {#if !disabled}<button type="button" class="btn ghost icon sm" aria-label="Remove volume" onclick={() => form.volumes.splice(i, 1)} data-testid="vol-remove"><Icon icon={X} size={14} /></button>{:else}<span></span>{/if}
            </div>
          {/each}
        </div>
        <p class="xs faint hint">Type <code class="mono">{'{{'}</code> for values of this container, e.g. <code class="mono">{'/app/helper/config/env/{{ container.env.NODE_ENV }}.ts'}</code>. <b>Kind</b>: what the image keeps at the path — a <b>Folder</b>, or one <b>File</b> (e.g. <code class="mono">/data/flows.json</code>). Containers mount a folder data volume on a Folder and a single-file volume on a File.</p>
      {/if}
    </div>
  </section>

  <section class="card">
    <div class="card-head"><span class="num">{start + 4}</span><h3>Health check</h3>{@render headExtra?.('healthcheck')}</div>
    <div class="card-body"><HealthCheckEditor bind:value={form.healthcheck} {portNames} issues={shown} {disabled} /></div>
  </section>

  <section class="card">
    <div class="card-head"><span class="num">{start + 5}</span><h3>Description</h3></div>
    <div class="card-body">
      <Field id="ri-desc" error={e('description')} aside={cnt}><textarea id="ri-desc" class="textarea" bind:value={form.description} rows="3" aria-label="Description" data-testid="ri-desc"></textarea></Field>
    </div>
  </section>
</fieldset>

{#snippet cnt()}{form.description.length} / 2000{/snippet}

<style>
  .spec {
    border: 0;
    margin: 0;
    padding: 0;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: var(--sp-5, 20px);
  }
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
  .help {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-size: var(--fs-xs);
    color: var(--accent-text);
    cursor: help;
  }
  .hint {
    margin-top: 8px;
  }
  .rows {
    display: flex;
    flex-direction: column;
    gap: 6px;
    overflow-x: auto;
  }
  .rh,
  .rr {
    display: grid;
    gap: 8px;
    align-items: start;
    min-width: 640px;
  }
  .rh {
    font-size: var(--fs-xs);
    color: var(--text-3);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .ports {
    grid-template-columns: 150px 150px 110px 1fr 30px;
  }
  .env {
    grid-template-columns: 170px minmax(280px, 1.5fr) 64px 70px 1fr 30px;
    min-width: 860px;
  }
  .rr.env .check {
    min-height: var(--control-h-sm);
    font-size: var(--fs-sm);
  }
  .vol {
    grid-template-columns: 150px minmax(300px, 1.6fr) 230px 1fr 30px;
    min-width: 960px;
  }
  .rr.vol.focus {
    outline: 2px solid var(--accent);
    outline-offset: 3px;
    border-radius: var(--radius-sm, 6px);
  }
  .ren {
    display: block;
    margin-top: 3px;
    font-size: var(--fs-xs);
    color: var(--accent-text);
  }
  .ren code {
    font-family: var(--font-mono);
  }
  .rnote {
    display: flex;
    gap: 6px;
    align-items: center;
    font-size: var(--fs-xs);
    color: var(--amber-text, var(--text-2));
    padding: 0 0 4px 4px;
    min-width: 640px;
  }
</style>
