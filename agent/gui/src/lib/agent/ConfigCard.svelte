<script lang="ts">
  /**
   * agent.json (round I): masked view (pretty JSON, copy) + editor of the safe settings (tools.hostShell,
   * tools.containerExec, containerNames, edgeHttpPort, guiAliasPort) validated live with validateAgentSettings, a diff of
   * the changed keys, typed site slug, "Save and restart" (default) or "Save only".
   */
  import type { AgentConfigView, AgentEditableSettings, PatchAgentConfigResponse } from '@opm/shared/agent-self';
  import { validateAgentSettings } from '@opm/shared/agent-self';
  import { untrack } from 'svelte';
  import { errMessage, errCode } from '$lib/api';
  import { toasts } from '$lib/state/toast.svelte';
  import { session } from '$lib/state/session.svelte';
  import { jsonPretty } from '$lib/format';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import InfoTip from '$lib/ui/InfoTip.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import { agentApi } from './api';
  import { copyText } from './copy';

  interface Props {
    site: string;
    agentPort: number;
    view: AgentConfigView;
    /** why writes are blocked right now (busy agent), else null */
    blocked: string | null;
    onsaved: (r: PatchAgentConfigResponse, restart: boolean) => void;
    onreload: () => void;
  }
  let { site, agentPort, view, blocked, onsaved, onreload }: Props = $props();

  type Draft = { hostShell: boolean; containerExec: boolean; devTools: boolean | undefined; containerNames: 'plain' | 'prefixed'; edgeNone: boolean; edge: string; aliasNone: boolean; alias: string };
  const toDraft = (s: AgentEditableSettings): Draft => ({ hostShell: s.tools.hostShell, containerExec: s.tools.containerExec, devTools: s.tools.devTools, containerNames: s.containerNames, edgeNone: s.edgeHttpPort === null, edge: s.edgeHttpPort === null ? '' : String(s.edgeHttpPort), aliasNone: s.guiAliasPort === null, alias: s.guiAliasPort === null ? '' : String(s.guiAliasPort) });
  const portOf = (none: boolean, t: string): number | null => (none ? null : t.trim() === '' ? NaN : Number(t.trim()));
  const fromDraft = (d: Draft): AgentEditableSettings => ({ tools: { hostShell: d.hostShell, containerExec: d.containerExec, ...(d.devTools !== undefined ? { devTools: d.devTools } : {}) }, containerNames: d.containerNames, edgeHttpPort: portOf(d.edgeNone, d.edge), guiAliasPort: portOf(d.aliasNone, d.alias) });

  let d = $state<Draft>(untrack(() => toDraft(view.settings)));
  let seen = $state(untrack(() => JSON.stringify(view.settings)));
  $effect(() => {
    // a fresh view from the server (after a save / reload) resets the editor when it is not dirty
    const k = JSON.stringify(view.settings);
    if (k !== seen) {
      seen = k;
      d = toDraft(view.settings);
    }
  });
  let confirm = $state('');
  let busy = $state<'' | 'restart' | 'save'>('');
  let serverErr = $state<string | null>(null);
  let showRaw = $state(false);

  const next = $derived(fromDraft(d));
  const issues = $derived(validateAgentSettings(next, { agentPort, previous: view.settings }));
  const errs = $derived(issues.filter((i) => i.severity === 'error'));
  const at = (path: string) => issues.filter((i) => i.path === path);
  const flat = (x: AgentEditableSettings): Record<string, unknown> => ({ 'tools.hostShell': x.tools.hostShell, 'tools.containerExec': x.tools.containerExec, 'tools.devTools': x.tools.devTools ?? true, containerNames: x.containerNames, edgeHttpPort: x.edgeHttpPort, guiAliasPort: x.guiAliasPort });
  const changes = $derived.by(() => {
    const a = flat(view.settings);
    const b = flat(next);
    return Object.keys(b)
      .filter((k) => !(Number.isNaN(b[k]) && Number.isNaN(a[k])) && a[k] !== b[k])
      .map((k) => ({ k, from: a[k], to: b[k] }));
  });
  const show = (v: unknown) => (v === null ? 'none' : typeof v === 'boolean' ? (v ? 'on' : 'off') : Number.isNaN(v) ? '(empty)' : String(v));
  const ro = $derived(!view.writable);
  const canSave = $derived(!ro && !blocked && changes.length > 0 && errs.length === 0 && confirm === site && !busy);
  const why = $derived(ro ? 'agent.json is read-only' : blocked ? blocked : changes.length === 0 ? 'No changes' : errs.length ? `${errs.length} ${errs.length === 1 ? 'error' : 'errors'} to fix` : confirm !== site ? `Type ${site} to confirm` : '');

  async function save(restart: boolean) {
    if (!canSave) return;
    if (!session.actor) {
      session.askActor = true;
      toasts.info('Say who is working first', 'The change is written to the activity log with your email.');
      return;
    }
    busy = restart ? 'restart' : 'save';
    serverErr = null;
    try {
      const patch: Record<string, unknown> = {};
      for (const c of changes) {
        if (c.k.startsWith('tools.')) patch.tools = { ...((patch.tools as object) ?? {}), [c.k.slice(6)]: c.to };
        else patch[c.k] = c.to;
      }
      const r = await agentApi.patchAgentConfig({ siteSlug: site, body: { settings: patch, confirm: site, restart } });
      confirm = '';
      onsaved(r, restart);
    } catch (e) {
      const code = errCode(e);
      serverErr = code === 'forbidden' ? `agent.json is not writable: ${errMessage(e)}` : code === 'locked' || code === 'conflict' ? `${errMessage(e)} (409 ${code})` : errMessage(e);
    } finally {
      busy = '';
    }
  }
  const ISSUE_CLS = { error: 'field-err', warning: 'field-warn', info: 'field-info' } as const;
</script>

{#snippet issueList(path: string)}
  {#each at(path) as i (i.code + i.message)}<span class={ISSUE_CLS[i.severity]} data-testid="issue-{path}-{i.severity}">{i.message}</span>{/each}
{/snippet}

<section class="card" data-testid="agent-config">
  <div class="card-head">
    <h2 class="row tight"><Icon name="braces" size={16} />agent.json <span class="mono muted small">{view.path}</span></h2>
    <div class="row">
      {#if view.restartPending}<span class="badge warn" data-testid="restart-pending" title="agent.json changed on disk since the agent started; a restart applies it">restart pending</span>{/if}
      {#if ro}<span class="badge outline"><Icon name="lock" size={12} />read-only</span>{/if}
      <Btn size="sm" variant="ghost" icon={showRaw ? 'eyeOff' : 'eye'} onclick={() => (showRaw = !showRaw)} data-testid="config-raw-toggle">{showRaw ? 'Hide file' : 'View file'}</Btn>
    </div>
  </div>
  <div class="card-body stack">
    {#if showRaw}
      <div class="raw">
        <div class="row between">
          <span class="small muted">As stored on the server. Secrets are masked (<code>••••</code> + last 4).</span>
          <Btn size="xs" icon="copy" onclick={() => copyText(jsonPretty(view.masked), 'agent.json (masked)')} data-testid="config-copy">Copy</Btn>
        </div>
        <pre class="code" data-testid="config-masked">{jsonPretty(view.masked)}</pre>
      </div>
    {/if}
    {#if ro}
      <div class="banner warn small" data-testid="config-readonly"><Icon name="lock" /><div class="grow"><strong>The editor is read-only.</strong> {view.writableReason ?? 'The agent cannot write agent.json.'}</div></div>
    {:else}
      <p class="card-hint small muted" style="margin:0">Only these settings can change here. Everything else: edit <code>{view.path}</code> on the server and restart the agent. Saving keeps <code>agent.json.bak</code>.</p>
    {/if}

    <div class="sets">
      <div class="set">
        <label class="switch"><input type="checkbox" bind:checked={d.hostShell} disabled={ro} data-testid="set-hostShell" />Host shell</label>
        <span class="hint">Agent › Host shell can open a shell on the server (after the typed site slug). <code>tools.hostShell</code></span>
        {@render issueList('tools.hostShell')}
      </div>
      <div class="set">
        <label class="switch"><input type="checkbox" bind:checked={d.containerExec} disabled={ro} data-testid="set-containerExec" />Container terminal</label>
        <span class="hint">A container's Terminal tab can open a shell inside it. <code>tools.containerExec</code></span>
        {@render issueList('tools.containerExec')}
      </div>
      {#if d.devTools !== undefined}
        <div class="set" id="set-devtools">
          <label class="switch"><input type="checkbox" bind:checked={d.devTools} disabled={ro} data-testid="set-devTools" />Dev tools</label>
          <span class="hint">Dev tools (SQL, Redis, MQTT, HTTP client) run queries on this server; every query is logged. <code>tools.devTools</code></span>
          {@render issueList('tools.devTools')}
        </div>
      {/if}
      <div class="set">
        <span class="label">Container names <InfoTip text="plain: the container is named like its containerSlug (hello-api). prefixed: opm-{site}-hello-api. The running containers are renamed at the restart." /></span>
        <SegmentedControl size="sm" label="Container names" value={d.containerNames} options={[{ value: 'plain', label: 'plain' }, { value: 'prefixed', label: 'prefixed' }]} onchange={(v) => (d.containerNames = v)} disabled={ro} testid="set-containerNames" />
        {@render issueList('containerNames')}
      </div>
      <div class="set">
        <span class="label">Default edge HTTP port <InfoTip text="The port the default edge answers on before the first update brings the site's own edge (agent.json edgeHttpPort)." /></span>
        <div class="row nowrap">
          <input class="input mono port" type="text" inputmode="numeric" bind:value={d.edge} disabled={ro || d.edgeNone} placeholder={d.edgeNone ? 'none' : '80'} aria-label="Default edge HTTP port" data-testid="set-edgeHttpPort" />
          <label class="check small"><input type="checkbox" bind:checked={d.edgeNone} disabled={ro} data-testid="set-edgeHttpPort-none" />none</label>
        </div>
        {@render issueList('edgeHttpPort')}
      </div>
      <div class="set">
        <span class="label">GUI alias port <InfoTip text="An extra port that answers with this GUI (agent.json guiAliasPort). The agent port {agentPort} always answers too." /></span>
        <div class="row nowrap">
          <input class="input mono port" type="text" inputmode="numeric" bind:value={d.alias} disabled={ro || d.aliasNone} placeholder={d.aliasNone ? 'none' : '8090'} aria-label="GUI alias port" data-testid="set-guiAliasPort" />
          <label class="check small"><input type="checkbox" bind:checked={d.aliasNone} disabled={ro} data-testid="set-guiAliasPort-none" />none</label>
        </div>
        {@render issueList('guiAliasPort')}
      </div>
    </div>

    {#if changes.length}
      <div class="diff" data-testid="config-diff">
        <div class="section-title">Changes ({changes.length})</div>
        <div class="table-wrap">
          <table class="table">
            <thead><tr><th>Setting</th><th>Now</th><th>After saving</th></tr></thead>
            <tbody>
              {#each changes as c (c.k)}<tr><td class="mono small">{c.k}</td><td class="mono small muted">{show(c.from)}</td><td class="mono small strong">{show(c.to)}</td></tr>{/each}
            </tbody>
          </table>
        </div>
      </div>
      <div class="field confirm">
        <label for="cfg-confirm">Type the site slug <code>{site}</code> to save</label>
        <input id="cfg-confirm" class="input mono" bind:value={confirm} autocomplete="off" spellcheck="false" disabled={ro} data-testid="config-confirm" />
        <span class="hint">“Save and restart” applies the settings now (the agent restarts; the containers keep running). “Save only” applies them at the next restart.</span>
      </div>
    {/if}
    {#if serverErr}<div class="banner bad small" data-testid="config-error"><Icon name="alert" /><div class="grow">{serverErr}</div><Btn size="xs" onclick={() => ((serverErr = null), onreload())}>Reload</Btn></div>{/if}
    <div class="row end">
      {#if changes.length}<Btn variant="ghost" onclick={() => ((d = toDraft(view.settings)), (confirm = ''))} disabled={!!busy}>Discard</Btn>{/if}
      {#if why && changes.length}<span class="small muted" data-testid="config-why">{why}</span>{/if}
      <Btn onclick={() => save(false)} disabled={!canSave} busy={busy === 'save'} data-testid="config-save-only">Save only</Btn>
      <Btn variant="primary" icon="rotate" onclick={() => save(true)} disabled={!canSave} busy={busy === 'restart'} data-testid="config-save-restart">Save and restart</Btn>
    </div>
  </div>
</section>

<style>
  .row.tight {
    gap: 6px;
  }
  .sets {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(min(100%, 260px), 1fr));
    gap: 16px 24px;
  }
  .set {
    display: flex;
    flex-direction: column;
    gap: 5px;
    min-width: 0;
  }
  .set .label {
    display: inline-flex;
    gap: 5px;
    align-items: center;
  }
  .port {
    width: 110px;
  }
  .row.nowrap {
    flex-wrap: nowrap;
  }
  .raw {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .raw .code {
    max-height: 360px;
  }
  .confirm {
    max-width: 420px;
  }
</style>
