<script lang="ts">
  /**
   * Site → Dev tools (§21 #97): the dev-tool presets of a site (SiteConfig.devTools). Each preset is a connection the
   * agent GUI lists ready-to-use (Dev tools → connections, read-only there). Add = kind picker (modal) → DevToolDrawer;
   * edit / duplicate / delete (confirm). Every change goes into the site working copy (Save template / Save draft); an
   * empty list removes the key (never devTools: []).
   */
  import type { DevToolKind, DevToolPreset, Site, ValidationIssue } from '@opm/shared';
  import { DEVTOOL_KINDS, DEVTOOL_KIND_LABEL, DEVTOOL_PRESETS_MAX, devToolTargetText, newDevToolPreset, normalizeStoredDevToolPreset } from '@opm/shared';
  import Button from '../../ui/Button.svelte';
  import Modal from '../../ui/Modal.svelte';
  import ConfirmDialog from '../../ui/ConfirmDialog.svelte';
  import EmptyState from '../../ui/EmptyState.svelte';
  import Icon from '../../ui/Icon.svelte';
  import { Plus, Pencil, Trash2, Wrench, Info, Lock, CopyPlus, CircleAlert, TriangleAlert, Eye } from '../../ui/icons';
  import DevToolDrawer from './DevToolDrawer.svelte';
  import { presetSources } from '$opm/forms/devtools/devtools';
  import { useConfigHost } from '$opm/forms/host';
  import { KIND_ICON, KIND_BLURB, authSummary, listIssues, presetIssues, setSiteDevTools, agentSupportsDevTools, devToolsAgentNeed, devToolsAgentWarning } from './devtools';
  import { sevClass } from '../../util/issues';
  import type { AnyScope } from '../scope.svelte';

  let { scope, site, issues, addOpen = $bindable(false) }: { scope: AnyScope; site: Site; issues: ValidationIssue[]; addOpen?: boolean } = $props();
  const host = useConfigHost();
  const ro = $derived(!scope.canWrite);
  const presets = $derived(site.config.devTools ?? []);
  const slugs = $derived(presets.map((p) => p.presetSlug));
  const full = $derived(presets.length >= DEVTOOL_PRESETS_MAX);
  const top = $derived(listIssues(issues, site.siteSlug));
  const agentVersion = $derived(scope.kind === 'deployment' ? (scope.status?.sites.find((s) => s.siteSlug === site.siteSlug)?.agentVersion ?? null) : null);
  const need = $derived(devToolsAgentNeed(presets)); // §21 #99: containers.*.env / #103: static values need a newer agent
  const oldAgent = $derived(!!agentVersion && !agentSupportsDevTools(agentVersion, need.min));

  // drawer
  let drawerOpen = $state(false);
  let initial = $state<DevToolPreset | null>(null);
  let editing = $state(false);
  let viewOnly = $state(false);
  const taken = $derived(editing && initial ? slugs.filter((s) => s !== initial!.presetSlug) : slugs);

  function startNew(kind: DevToolKind) {
    const p = newDevToolPreset(kind, slugs);
    p.target = presetSources(host, site.siteSlug).defaultTarget(kind, p.target.kind);
    initial = p;
    editing = false;
    viewOnly = false;
    addOpen = false;
    drawerOpen = true;
  }
  function edit(p: DevToolPreset) {
    initial = p;
    editing = true;
    viewOnly = ro;
    drawerOpen = true;
  }
  function duplicate(p: DevToolPreset) {
    const c = structuredClone($state.snapshot(p)) as DevToolPreset;
    let slug = `${p.presetSlug}-copy`.slice(0, 63);
    for (let i = 2; slugs.includes(slug); i++) slug = `${p.presetSlug}-copy-${i}`.slice(0, 63);
    c.presetSlug = slug;
    c.name = `${p.name} (copy)`.slice(0, 80);
    initial = c;
    editing = false;
    viewOnly = false;
    drawerOpen = true;
  }
  function save(p: DevToolPreset) {
    const list = presets.map((x) => $state.snapshot(x) as DevToolPreset);
    if (editing && initial) setSiteDevTools(site.config, list.map((x) => (x.presetSlug === initial!.presetSlug ? p : x)));
    else setSiteDevTools(site.config, [...list, p]);
  }

  let rm = $state<DevToolPreset | null>(null);
  let rmOpen = $state(false);
  function doDelete() {
    const slug = rm!.presetSlug;
    setSiteDevTools(
      site.config,
      presets.filter((p) => p.presetSlug !== slug).map((x) => normalizeStoredDevToolPreset($state.snapshot(x))),
    );
  }
  const counts = (p: DevToolPreset, i: number) => {
    const l = presetIssues(issues, site.siteSlug, p.presetSlug, i);
    return { e: l.filter((x) => x.severity === 'error'), w: l.filter((x) => x.severity === 'warning') };
  };
</script>

<div class="banner info small" data-testid="devtools-info">
  <Icon icon={Info} size={15} class="icon" />
  <div>
    <b>Dev tools presets</b> are connections the agent GUI lists ready-to-use under <b>Dev tools</b> (HTTP client, PostgreSQL, SQLite, Cassandra, Redis, MQTT) — the agent runs every query on the site server and logs it to activities. Credentials: a <b>secret</b> (<code class="inline">{'{{ secret.x }}'}</code>, recommended), a container env or an expression — resolved on the site — or a typed <b>Value</b> (stored in plain text in the config: use a secret for production). Operators can always add manual connections on the agent.
  </div>
</div>
{#if oldAgent}
  <div class="banner warn small" data-testid="devtools-agent-warning"><Icon icon={TriangleAlert} size={15} class="icon" /><div>{devToolsAgentWarning({ siteSlug: site.siteSlug, agentVersion: agentVersion!, envRefs: need.envRefs, literals: need.literals })}</div></div>
{/if}
{#each top as x, k (k)}<div class="xs {sevClass(x.severity)}">{x.message}</div>{/each}

{#if !presets.length}
  <EmptyState icon={Wrench} title="No dev tool presets" text="Presets are connections the agent GUI lists ready-to-use (e.g. this site's PostgreSQL or Redis); credentials only via secrets; operators can always add manual connections on the agent.">
    {#snippet action()}{#if !ro}<Button variant="primary" icon={Plus} onclick={() => (addOpen = true)} data-testid="devtools-empty-add">Add preset</Button>{/if}{/snippet}
  </EmptyState>
{:else}
  <div class="table-wrap">
    <table class="table" data-testid="devtools-table">
      <thead><tr><th>Preset</th><th>Kind</th><th>Target</th><th class="hide-sm">Auth</th><th class="actions"><span class="sr-only">Actions</span></th></tr></thead>
      <tbody>
        {#each presets as p, i (p.presetSlug)}
          {@const c = counts(p, i)}
          <tr data-testid="devtool-row-{p.presetSlug}" class:err={c.e.length > 0}>
            <td>
              <div class="name">
                <span class="kic k-{p.kind}"><Icon icon={KIND_ICON[p.kind]} size={14} /></span>
                <div class="min">
                  <div class="row wrap tight">
                    <span class="strong">{p.name}</span>
                    {#if p.readOnly}<span class="badge gray" data-testid="devtool-ro-{p.presetSlug}"><Icon icon={Lock} size={10} />read-only</span>{/if}
                    {#if c.e.length}<span class="badge red" title={c.e.map((x) => x.message).join('\n')} data-testid="devtool-errors-{p.presetSlug}"><Icon icon={CircleAlert} size={10} />{c.e.length}</span>{/if}
                    {#if c.w.length}<span class="badge amber" title={c.w.map((x) => x.message).join('\n')} data-testid="devtool-warnings-{p.presetSlug}"><Icon icon={TriangleAlert} size={10} />{c.w.length}</span>{/if}
                  </div>
                  <div class="xs faint mono">p:{p.presetSlug}</div>
                  {#if p.description}<div class="xs muted desc">{p.description}</div>{/if}
                  {#each [...c.e, ...c.w] as x, k (k)}<div class="xs {sevClass(x.severity)}" data-testid="devtool-issue-{p.presetSlug}">{x.message}</div>{/each}
                </div>
              </div>
            </td>
            <td class="small nowrap">{DEVTOOL_KIND_LABEL[p.kind]}</td>
            <td class="small mono tgt" data-testid="devtool-target-{p.presetSlug}">{devToolTargetText(p.target)}</td>
            <td class="small hide-sm auth" data-testid="devtool-auth-{p.presetSlug}">{authSummary(p)}</td>
            <td class="actions nowrap">
              {#if ro}
                <Button size="sm" variant="ghost" icon={Eye} tip="View" aria-label="View {p.presetSlug}" onclick={() => edit(p)} data-testid="dt-view-{p.presetSlug}" />
              {:else}
                <Button size="sm" variant="ghost" icon={Pencil} tip="Edit" aria-label="Edit {p.presetSlug}" onclick={() => edit(p)} data-testid="dt-edit-{p.presetSlug}" />
                <Button size="sm" variant="ghost" icon={CopyPlus} tip={full ? `At most ${DEVTOOL_PRESETS_MAX} presets` : 'Duplicate'} disabled={full} aria-label="Duplicate {p.presetSlug}" onclick={() => duplicate(p)} data-testid="dt-dup-{p.presetSlug}" />
                <Button size="sm" variant="ghost" icon={Trash2} tip="Delete" aria-label="Delete {p.presetSlug}" onclick={() => ((rm = p), (rmOpen = true))} data-testid="dt-delete-{p.presetSlug}" />
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
  <div class="xs faint foot">{presets.length} of {DEVTOOL_PRESETS_MAX} presets · shown in the agent GUI as <span class="mono">p:&lt;id&gt;</span> once a revision with them is applied (agent 0.11.0+).</div>
{/if}

<Modal bind:open={addOpen} title="Add a dev tool preset" subtitle="Site {site.siteSlug} · pick the kind of service" size="md" testid="devtool-kind-picker">
  <div class="kinds">
    {#each DEVTOOL_KINDS as k (k)}
      <button type="button" class="kind" onclick={() => startNew(k)} disabled={full} data-testid="dt-kind-{k}">
        <span class="kic k-{k}"><Icon icon={KIND_ICON[k]} size={18} /></span>
        <span class="kt"><span class="strong">{DEVTOOL_KIND_LABEL[k]}</span><span class="xs muted">{KIND_BLURB[k]}</span></span>
      </button>
    {/each}
  </div>
  {#if full}<div class="xs text-red">This site has {DEVTOOL_PRESETS_MAX} presets (the maximum).</div>{/if}
  {#snippet footer()}<Button onclick={() => (addOpen = false)}>Cancel</Button>{/snippet}
</Modal>

<DevToolDrawer bind:open={drawerOpen} {scope} {site} {initial} {editing} {taken} readonly={viewOnly} onsave={save} />

<ConfirmDialog bind:open={rmOpen} title="Delete preset {rm?.name ?? ''}?" confirmLabel="Delete preset" danger onconfirm={doDelete} testid="delete-devtool">
  {#snippet body()}
    <p class="muted small">The preset <span class="mono">p:{rm?.presetSlug}</span> is removed from the site config (not committed until Commit…). After the next applied revision the agent no longer lists it; manual connections, saved queries and history on the agent are not touched.</p>
  {/snippet}
</ConfirmDialog>

<style>
  .banner {
    margin-bottom: var(--sp-3);
  }
  .name {
    display: flex;
    gap: 10px;
    align-items: flex-start;
  }
  .min {
    min-width: 0;
  }
  .tight {
    gap: 6px;
  }
  .kic {
    --kc: var(--accent);
    flex: none;
    display: inline-flex;
    width: 28px;
    height: 28px;
    align-items: center;
    justify-content: center;
    border-radius: 7px;
    background: color-mix(in srgb, var(--kc) 15%, transparent);
    color: var(--kc);
  }
  /* mid tones: readable on light and dark surfaces */
  .k-postgres {
    --kc: #4f86c6;
  }
  .k-redis {
    --kc: #e0524a;
  }
  .k-mqtt {
    --kc: #b45bb4;
  }
  .k-cassandra {
    --kc: #2b9bc9;
  }
  .k-sqlite {
    --kc: #3b8fd6;
  }
  .k-http {
    --kc: #e08a2b;
  }
  .desc {
    max-width: 360px;
    overflow-wrap: anywhere;
  }
  .tgt {
    max-width: 260px;
    overflow-wrap: anywhere;
  }
  .auth {
    max-width: 320px;
    overflow-wrap: anywhere;
  }
  tr.err td {
    background: color-mix(in srgb, var(--red) 4%, transparent);
  }
  .badge :global(svg) {
    margin-right: 2px;
  }
  .foot {
    margin-top: 6px;
  }
  .kinds {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 8px;
  }
  @media (max-width: 520px) {
    .kinds {
      grid-template-columns: minmax(0, 1fr);
    }
  }
  .kind {
    display: flex;
    gap: 10px;
    align-items: center;
    text-align: left;
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    cursor: pointer;
    color: inherit;
    font: inherit;
  }
  .kind:hover:not(:disabled),
  .kind:focus-visible {
    border-color: var(--accent);
    background: var(--accent-soft);
  }
  .kind:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .kt {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }
  @media (max-width: 720px) {
    .hide-sm {
      display: none;
    }
  }
</style>
