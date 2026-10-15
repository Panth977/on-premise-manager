<script lang="ts">
  /**
   * A dev tools connection (§7.6) in a side drawer — the shared DevToolForm ($opm, SPEC §21 #102: the SAME form as the
   * cloud preset drawer). Manual connections: create / edit (variant connection: kind picker when new, a credential "Value"
   * stored on the agent, RevealValue). Presets: the same form read-only (variant preset, mode read) + Test / Close; a
   * preset's static password / token value (§21 #103, MASK in reads) is revealed below the form (a read-only form disables
   * its own buttons).
   * "Test" runs testDraftConnection on the form (or testConnection when unchanged / a preset).
   */
  import { untrack } from 'svelte';
  import { devToolLiteralFields, type DevToolConnection, type DevToolKind, type DevToolsInfo, type TestConnectionResponse } from '@opm/shared/devtools';
  import type { ValidationIssue } from '@opm/shared/types';
  import DevToolForm from '$opm/forms/devtools/DevToolForm.svelte';
  import { blankDraft, connectionIssues, connectionToDraft, draftToConnectionInput, infoSources, type DevToolDraft } from '$opm/forms/devtools/devtools';
  import { errMessage, issuesOf } from '$lib/api';
  import { toasts } from '$lib/state/toast.svelte';
  import Drawer from '$lib/ui/Drawer.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import RevealValue from '$lib/ui/RevealValue.svelte';
  import { dev } from './api';

  interface Props {
    open: boolean;
    site: string;
    info: DevToolsInfo;
    /** null = new connection */
    conn: DevToolConnection | null;
    /** kind of a new connection (the tool page it was opened from) */
    kind?: DevToolKind;
    /** kinds offered for a new connection */
    kinds?: DevToolKind[];
    onsaved?: (c: DevToolConnection) => void;
  }
  let { open = $bindable(false), site, info, conn, kind = 'postgres', kinds, onsaved }: Props = $props();

  const sources = $derived(infoSources(info, site));
  let d = $state<DevToolDraft>(blankDraft('postgres'));
  let submitted = $state(false);
  let valid = $state(true);
  let server = $state<ValidationIssue[]>([]);
  let busy = $state(false);
  let testing = $state(false);
  let test = $state<TestConnectionResponse | null>(null);
  let initial = '';
  $effect(() => {
    if (!open) return;
    const c = conn;
    untrack(() => load(c));
  });
  function load(c: DevToolConnection | null) {
    d = c ? connectionToDraft(c) : blankDraft(kind, sources);
    initial = JSON.stringify(d);
    submitted = false;
    server = [];
    test = null;
  }
  const isNew = $derived(!conn);
  const isPreset = $derived(conn?.source === 'preset');
  const dirty = $derived(JSON.stringify(d) !== initial);
  const issues = $derived(isPreset ? [] : [...connectionIssues(d), ...server]);

  async function save() {
    submitted = true;
    if (!valid) return;
    busy = true;
    try {
      const body = draftToConnectionInput($state.snapshot(d) as DevToolDraft);
      const out = conn ? await dev.updateConnection({ siteSlug: site, connectionId: conn.id, body }) : await dev.createConnection({ siteSlug: site, body });
      toasts.ok(conn ? `Saved ${out.name}` : `Connection ${out.name} created`);
      open = false;
      onsaved?.(out);
    } catch (e) {
      const list = issuesOf(e);
      server = list.map((i) => ({ ...i, severity: i.severity ?? 'error' }));
      if (!list.length) toasts.err(e, 'Not saved');
    } finally {
      busy = false;
    }
  }
  async function runTest() {
    if (!isPreset) {
      submitted = true;
      if (connectionIssues(d).some((i) => i.severity === 'error')) return;
    }
    testing = true;
    test = null;
    try {
      const body = draftToConnectionInput($state.snapshot(d) as DevToolDraft);
      test = conn && (!dirty || isPreset) ? await dev.testConnection({ siteSlug: site, connectionId: conn.id }) : await dev.testDraftConnection({ siteSlug: site, body: { ...body, name: body.name || 'draft' }, query: conn ? { connectionId: conn.id } : {} });
    } catch (e) {
      test = { ok: false, latencyMs: 0, serverVersion: null, resolvedTarget: null, message: errMessage(e) };
    } finally {
      testing = false;
    }
  }
  const revealOf = (field: 'password' | 'token') => (conn && !isNew ? () => dev.revealCredential({ siteSlug: site, connectionId: conn!.id, query: { field } }) : null);
  /** §21 #103: the preset's static credential fields (the form shows them masked; Reveal is below the form) */
  const presetLiterals = $derived(conn && isPreset ? devToolLiteralFields(conn) : []);
</script>

<Drawer bind:open title={isNew ? 'Add connection' : isPreset ? `Preset ${conn?.id.replace(/^p:/, '') ?? ''}` : `Edit ${conn?.name}`} subtitle={isPreset ? 'Read-only: presets come from the cloud configuration (template / deployment).' : `Stored on the agent of site ${site}. Queries run on that server.`} testid="conn-drawer">
  {#if open}
    <DevToolForm bind:value={d} bind:valid variant={isPreset ? 'preset' : 'connection'} mode={isPreset ? 'read' : isNew ? 'create' : 'edit'} {sources} {issues} {submitted} {kinds} siteSlug={site} reveal={conn && !isNew && !isPreset ? revealSnippet : undefined} />
    {#if isPreset && conn}
      {#each presetLiterals as f (f)}
        <div class="static-reveal" data-testid="preset-{f}-static">
          <span class="small">Static {f} value of this preset (stored in plain text in the cloud config; masked here)</span>
          {@render revealSnippet(f)}
        </div>
      {/each}
    {/if}
  {/if}
  {#if test}
    <div class="banner {test.ok ? 'ok' : 'bad'} test" data-testid="conn-test-result">
      <Icon name={test.ok ? 'check' : 'alert'} />
      <div class="grow">
        <strong>{test.ok ? 'Connected' : 'Failed'}</strong>{test.ok ? ` in ${test.latencyMs} ms` : ''}{test.serverVersion ? ` · ${test.serverVersion}` : ''}
        {#if test.resolvedTarget}<div class="mono small">{test.resolvedTarget}</div>{/if}
        {#if !test.ok}<div class="small">{test.message}</div>{/if}
      </div>
    </div>
  {/if}
  {#snippet footer()}
    <Btn icon="plug" busy={testing} onclick={runTest} data-testid="conn-test">Test</Btn>
    <span class="grow"></span>
    <Btn variant="ghost" onclick={() => (open = false)}>{isPreset ? 'Close' : 'Cancel'}</Btn>
    {#if !isPreset}<Btn variant="primary" {busy} onclick={save} data-testid="conn-save">{isNew ? 'Create' : 'Save'}</Btn>{/if}
  {/snippet}
</Drawer>

{#snippet revealSnippet(field: 'password' | 'token')}
  {@const r = revealOf(field)}
  {#if r}<RevealValue reveal={r} label="{field} of this connection" testid="dt-{field}-reveal" />{/if}
{/snippet}

<style>
  .test {
    margin-top: 12px;
  }
  .static-reveal {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    margin-top: 10px;
  }
  .grow {
    flex: 1;
  }
</style>
