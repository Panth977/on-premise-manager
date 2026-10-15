<script lang="ts">
  import type { Site } from '@opm/shared';
  import { RE, RESERVED_SITE_SLUGS, checkExpr, defaultSiteConfig, LIMITS } from '@opm/shared';
  import { goto } from '$app/navigation';
  import Modal from '../ui/Modal.svelte';
  import Button from '../ui/Button.svelte';
  import Field from '../ui/Field.svelte';
  import HostField from '$opm/forms/site/HostField.svelte';
  import FormSummary from '../ui/FormSummary.svelte';
  import SlugInput from '../ui/SlugInput.svelte';
  import { issuesUnder } from '../util/issues';
  import type { ConfigScope } from './scope.svelte';

  /** + Add site (3 fields → modal): a new site with defaultSiteConfig(host), then open its editor. */
  let { open = $bindable(), scope }: { open: boolean; scope: ConfigScope } = $props();
  let slug = $state('');
  let label = $state('');
  let host = $state<string | null>(null);
  let touched = $state(false);
  $effect(() => {
    if (open) {
      slug = label = '';
      host = null;
      touched = false;
    }
  });
  const slugErr = $derived(!slug ? 'Required' : !RE.slug.test(slug) ? '2–40 characters: a–z, 0–9, "-", starting with a letter' : (RESERVED_SITE_SLUGS as readonly string[]).includes(slug) ? `"${slug}" is reserved (agent GUI routes)` : scope.config.sites.some((s) => s.siteSlug === slug) ? `site ${slug} exists` : null);
  const labelErr = $derived(!label.trim() ? 'Required' : label.length > LIMITS.label ? 'At most 80 characters' : null);
  const hostErr = $derived(host === null || !host.trim() ? null : host.includes('{{') ? (checkExpr(host, 'host')[0]?.message ?? null) : !RE.ipv4.test(host.trim()) && !RE.dnsName.test(host.trim()) && !host.includes(':') ? 'Not an IP or a DNS name' : null);
  const ok = $derived(!slugErr && !labelErr && !hostErr);
  const ctx = $derived({ deployment: { id: scope.deploymentId }, self: { slug, host: null }, var: Object.fromEntries(scope.config.variables.map((v) => [v.variableSlug, (v.value ?? null) as never])) });
  const server = $derived(issuesUnder(scope.saveIssues, `sites[${slug}]`));
  async function save(e: Event) {
    e.preventDefault();
    touched = true;
    if (!ok) return;
    const site: Site = { siteSlug: slug, label: label.trim(), config: defaultSiteConfig(host?.trim() ? host.trim() : null) };
    const items = [...scope.config.sites.map((s) => $state.snapshot(s) as Site), site];
    if (await scope.saveSection('sites', items, `Site ${slug} added`)) {
      open = false;
      goto(`${scope.base}/sites/${slug}/containers`);
    }
  }
</script>

<Modal bind:open title="Add site" subtitle="One machine with one agent. You add containers and routes in the site editor." busy={scope.saving} testid="add-site-modal">
  <form id="site-form" class="stack" onsubmit={save} novalidate>
    <FormSummary issues={server} />
    <Field label="Label" id="st-label" required error={touched ? labelErr : null}>
      <input id="st-label" class="input" bind:value={label} maxlength="80" placeholder="Application server" data-testid="site-label" />
    </Field>
    <Field label="Site slug" id="st-slug" required hint="From the label (pencil = edit), e.g. app, db, gate — fixed after create" error={touched || (slug && slugErr && !slugErr.startsWith('2–40')) ? slugErr : null}>
      <SlugInput id="st-slug" bind:value={slug} from={label} placeholder="app" testid="site-slug" />
    </Field>
    <Field label="Host" id="st-host" hint="Optional — leave auto-detect on unless you know the address (IP, DNS name or {'{{ var.… }}'}).">
      <HostField id="st-host" bind:value={host} {ctx} errors={touched && hostErr ? [hostErr] : []} placeholder="10.0.0.11 or {'{{ var.app-host }}'}" testid="site-host" />
    </Field>
  </form>
  {#snippet footer()}
    <Button onclick={() => (open = false)} disabled={scope.saving}>Cancel</Button>
    <Button type="submit" form="site-form" variant="primary" loading={scope.saving} disabled={touched && !ok} data-testid="site-save">Add site</Button>
  {/snippet}
</Modal>
