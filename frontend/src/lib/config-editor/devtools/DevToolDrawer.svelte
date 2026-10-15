<script lang="ts">
  /**
   * Add / edit / view a dev-tool preset (§21 #97 / #99) of one site — the shared DevToolForm ($opm, SPEC §21 #102: the same
   * form renders the agent's preset view and its manual connections) in a side drawer. Writes nothing itself:
   * `onsave(preset)` gets the stored shape (draftToPreset) and the caller puts it into the site in the browser change set
   * (§21 #105: committed with Commit…). Validation = shared validator over the config with this preset in place (presetIssues).
   */
  import type { DevToolPreset, Site } from '@opm/shared';
  import { DEVTOOL_KIND_LABEL } from '@opm/shared';
  import DevToolForm from '$opm/forms/devtools/DevToolForm.svelte';
  import { draftToPreset, presetIssues, presetSources, presetToDraft, type DevToolDraft } from '$opm/forms/devtools/devtools';
  import { useConfigHost } from '$opm/forms/host';
  import Drawer from '../../ui/Drawer.svelte';
  import Button from '../../ui/Button.svelte';
  import SecretDrawer from '../SecretDrawer.svelte';
  import type { AnyScope } from '../scope.svelte';

  type Props = {
    open: boolean;
    scope: AnyScope;
    site: Site;
    /** the preset to start from (new: newDevToolPreset + site defaults; edit: the stored one; duplicate: a copy) */
    initial: DevToolPreset | null;
    /** true = editing an existing preset (slug fixed) */
    editing: boolean;
    /** presetSlugs of the OTHER presets of the site */
    taken: string[];
    readonly?: boolean;
    onsave: (p: DevToolPreset) => void;
  };
  let { open = $bindable(), scope, site, initial, editing, taken, readonly = false, onsave }: Props = $props();
  const host = useConfigHost();

  let d = $state<DevToolDraft | null>(null);
  let valid = $state(true);
  $effect(() => {
    if (open && initial) {
      d = presetToDraft($state.snapshot(initial) as DevToolPreset);
    }
  });
  const sources = $derived(presetSources(host, site.siteSlug));
  const issues = $derived.by(() => {
    if (!d) return [];
    const list = presetIssues(host, site.siteSlug, $state.snapshot(d) as DevToolDraft, { editing, original: editing ? (initial?.presetSlug ?? null) : null });
    // a new preset whose slug collides with another preset of the site (taken = the OTHER presets)
    if (!editing && d.presetSlug && taken.includes(d.presetSlug) && !list.some((i) => i.code === 'V_DEVTOOL_DUP')) list.unshift({ path: 'presetSlug', code: 'V_DEVTOOL_DUP', message: `site ${site.siteSlug} already has a preset ${d.presetSlug}`, severity: 'error' });
    return list;
  });

  // "+ New secret…" opens the secret drawer nested; the new slug is applied when it is saved
  let secretOpen = $state(false);
  let applySecret: ((slug: string) => void) | null = null;
  function newSecret(_f: 'password' | 'token', apply: (slug: string) => void) {
    applySecret = apply;
    secretOpen = true;
  }

  function submit(e: Event) {
    e.preventDefault();
    if (!d || !valid || readonly) return;
    onsave(draftToPreset($state.snapshot(d) as DevToolDraft));
    open = false;
  }
  const title = $derived(readonly ? `Preset ${initial?.presetSlug ?? ''}` : editing ? `Edit preset ${initial?.presetSlug ?? ''}` : `New ${d ? DEVTOOL_KIND_LABEL[d.kind] : ''} preset`);
</script>

<Drawer bind:open {title} subtitle="Site {site.siteSlug} · listed ready-to-use in the agent GUI → Dev tools" testid="devtool-drawer">
  {#if d}
    <form id="devtool-form" onsubmit={submit} novalidate data-kind={d.kind}>
      <DevToolForm bind:value={d} bind:valid variant="preset" mode={readonly ? 'read' : editing ? 'edit' : 'create'} {sources} {issues} submitted={true} siteSlug={site.siteSlug} onnewsecret={scope.canWrite ? newSecret : undefined} />
    </form>
  {/if}
  {#snippet footer()}
    <Button onclick={() => (open = false)} data-testid="dt-cancel">{readonly ? 'Close' : 'Cancel'}</Button>
    {#if !readonly}<Button type="submit" form="devtool-form" variant="primary" disabled={!valid} data-testid="dt-save">{editing ? 'Apply' : 'Add preset'}</Button>{/if}
  {/snippet}
</Drawer>

<SecretDrawer bind:open={secretOpen} {scope} onsaved={(slug) => (applySecret?.(slug), (applySecret = null))} />
