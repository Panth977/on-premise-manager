<script lang="ts">
  /**
   * Site settings (SPEC §2.2 SiteConfig, §21 #51) — ONE form for the cloud site Settings tab (template + deployment) and
   * the agent Config › Settings (local edit), §21 #102. Cards: Address (Label · Host · Site depends · Not-found page)
   * and Agent ports (Agent port · Agent GUI alias port + the three ways to the agent GUI). The label is locked only
   * where the host locks it. Edits `site` in place (the caller's working copy). Test ids: site-label · site-host-* · site-depends ·
   * nf-picker · agent-port · alias-on(-input) · alias-port · gui-ways · site-issues.
   */
  import type { Site, ValidationIssue } from '@opm/shared/types';
  import { resolveSiteHosts, varValues } from '@opm/shared/validate';
  import Field from '../../primitives/Field.svelte';
  import MultiSelect from '../../primitives/MultiSelect.svelte';
  import NumberStepper from '../../primitives/NumberStepper.svelte';
  import Toggle from '../../primitives/Toggle.svelte';
  import Icon from '../../primitives/Icon.svelte';
  import Issues from '../../primitives/Issues.svelte';
  import HostField from './HostField.svelte';
  import NotFoundPagePicker from './NotFoundPagePicker.svelte';
  import { useConfigHost, type FormMode } from '../host';

  type Props = { site: Site; mode: FormMode; issues: readonly ValidationIssue[] };
  let { site, mode, issues }: Props = $props();
  const host = useConfigHost();
  const ro = $derived(mode === 'read' || host.readOnly);
  const cfg = $derived(host.config());
  const cb = $derived(`sites[${site.siteSlug}].config`);
  const detected = $derived(host.detectedHosts?.() ?? {});
  const hostCtx = $derived({ deployment: { id: host.deploymentId }, self: { slug: site.siteSlug, host: null }, var: varValues(cfg) });
  const resolved = $derived(resolveSiteHosts(cfg, host.deploymentId, { detected, placeholder: host.scope !== 'agent' })[site.siteSlug] ?? null);
  const hostText = $derived((resolved?.host || detected[site.siteSlug] || '<host>').replace(/<auto:[a-z0-9-]+>/g, '<machine-ip>'));
  const others = $derived(cfg.sites.filter((s) => s.siteSlug !== site.siteSlug).map((s) => ({ value: s.siteSlug, label: s.siteSlug, hint: s.label !== s.siteSlug ? s.label : undefined, icon: 'server' })));
  const at = (p: string) => issues.filter((i) => i.path === `${cb}.${p}`);
  const errs = (p: string) => at(p).filter((i) => i.severity === 'error').map((i) => i.message);
  const cycle = $derived(issues.filter((i) => i.code === 'V_CYCLE' && (i.path === 'sites' || i.path === `${cb}.depends`)));
  const rest = $derived(issues.filter((i) => i.path.startsWith(cb) && !['host', 'depends', 'notFoundPageId', 'agentPort', 'agentGuiAliasPort'].some((k) => i.path === `${cb}.${k}`) && !/\.(containers|edge|volumes|devTools)\b/.test(i.path)));
  const labelLock = $derived(host.lock(`sites[${site.siteSlug}].label`));
</script>

<fieldset class="form grid" disabled={ro} data-testid="site-settings">
  <section class="card">
    <div class="card-head"><h3>Address</h3></div>
    <div class="card-body stack">
      <Field label="Label" id="sh-label" lock={labelLock} hint="Shown instead of the slug in both GUIs." issues={at('label')}>
        <input id="sh-label" class="input" bind:value={site.label} maxlength="80" disabled={ro || !!labelLock} data-testid="site-label" />
      </Field>
      <Field label="Host" id="sh-host" hint="Optional. Address of this machine for containers of other sites and for users. Auto-detect uses the address the agent reports at install." issues={at('host').filter((i) => i.severity !== 'error' && i.code !== 'V_HOST_AUTO')}>
        <HostField id="sh-host" bind:value={site.config.host} ctx={hostCtx} {resolved} detected={detected[site.siteSlug] ?? null} errors={errs('host')} fallback={at('host').find((i) => i.code === 'V_HOST_AUTO')?.message ?? null} disabled={ro} testid="site-host" />
      </Field>
      <Field label="Site depends" id="sh-dep" hint="Sites this site needs first (an update applies dependencies first)." issues={[...at('depends'), ...cycle]}>
        <MultiSelect id="sh-dep" options={others} values={site.config.depends.map((d) => d.siteSlug)} onchange={(v) => (site.config.depends = v.map((s) => ({ siteSlug: s })))} disabled={ro} placeholder={others.length ? 'none' : 'No other sites'} ariaLabel="Site depends" testid="site-depends" />
      </Field>
      <Field label="Not-found page" id="sh-404" group hint="Served by the edge for unknown hosts / paths (404) and while a target is not ready (503)." issues={at('notFoundPageId')}>
        <NotFoundPagePicker value={site.config.notFoundPageId} onchange={(v) => (site.config.notFoundPageId = v)} disabled={ro} labelledby="sh-404-label" />
      </Field>
    </div>
  </section>

  <section class="card">
    <div class="card-head"><h3>Agent ports</h3></div>
    <div class="card-body stack">
      <div class="form-grid">
        <Field label="Agent port" id="sh-ap" required hint="Mesh + fallback GUI, 1024–65535" issues={at('agentPort')}>
          <NumberStepper id="sh-ap" value={Number.isNaN(site.config.agentPort) ? null : site.config.agentPort} onchange={(v) => (site.config.agentPort = v ?? NaN)} min={1024} max={65535} disabled={ro} label="Agent port" testid="agent-port" />
        </Field>
        <Field label="Agent GUI alias port" id="sh-alias" hint="1024–65535; off = no alias" issues={at('agentGuiAliasPort')}>
          <div class="row">
            <Toggle checked={site.config.agentGuiAliasPort !== null} onchange={(on) => (site.config.agentGuiAliasPort = on ? 8080 : null)} disabled={ro} label={site.config.agentGuiAliasPort === null ? 'off' : ''} ariaLabel="Agent GUI alias port on" testid="alias-on" />
            {#if site.config.agentGuiAliasPort !== null}<NumberStepper id="sh-alias" value={site.config.agentGuiAliasPort} onchange={(v) => (site.config.agentGuiAliasPort = v)} min={1024} max={65535} disabled={ro} label="Agent GUI alias port" testid="alias-port" />{/if}
          </div>
        </Field>
      </div>
      <div class="ways" data-testid="gui-ways">
        <div class="strong small">Three ways to the agent GUI of this site</div>
        <ul>
          <li><Icon name="globe" size={14} /><span>Edge (every host): <code>http://{hostText}{site.config.edge.httpPort === 80 ? '' : `:${site.config.edge.httpPort}`}/_opm/</code></span></li>
          <li><Icon name="monitor" size={14} /><span>GUI alias port: {#if site.config.agentGuiAliasPort}<code>http://{hostText}:{site.config.agentGuiAliasPort}/_opm/</code>{:else}<span class="faint">off</span>{/if}</span></li>
          <li><Icon name="network" size={14} /><span>Agent port (also before the first setup): <code>http://{hostText}:{site.config.agentPort}/_opm/</code></span></li>
        </ul>
      </div>
    </div>
  </section>
  {#if rest.length}<div class="full"><Issues issues={rest} title="Site validation" testid="site-issues" /></div>{/if}
</fieldset>

<style>
  .grid {
    display: grid;
    grid-template-columns: minmax(0, 1.2fr) minmax(0, 1fr);
    gap: var(--sp-4);
    align-items: start;
  }
  .full {
    grid-column: 1 / -1;
  }
  @media (max-width: 960px) {
    .grid {
      grid-template-columns: minmax(0, 1fr);
    }
  }
  .row {
    display: flex;
    gap: 8px;
    align-items: center;
  }
  .ways {
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    padding: 10px 12px;
  }
  .ways ul {
    list-style: none;
    padding: 0;
    margin: 8px 0 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
    font-size: var(--fs-sm);
  }
  .ways li {
    display: flex;
    gap: 8px;
    align-items: flex-start;
    color: var(--text-2);
    overflow-wrap: anywhere;
  }
  code {
    font-size: 11.5px;
  }
</style>
