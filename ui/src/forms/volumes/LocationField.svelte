<script lang="ts">
  /**
   * "Location on the server" of a file (one copy per site) or a site volume — ONE field for both GUIs (§21 #102):
   * Auto (recommended) = <agentHome>/files|volumes/<slug>, or a custom path ("./…" = relative to the agent home, or an
   * absolute path). Live check with the shared checkLocation (V_LOCATION). `agentHome` (agent GUI) adds the resolved path.
   * Test ids: {testid} · {testid}-mode-auto|path · {testid}-path · {testid}-err · {testid}-hint.
   */
  import type { StorageLocation } from '@opm/shared/types';
  import { Issues, checkLocation, locationLabel, locationPath } from '@opm/shared/validate';
  import SegmentedControl from '../../primitives/SegmentedControl.svelte';
  import Field from '../../primitives/Field.svelte';

  type Props = {
    value: StorageLocation;
    kind: 'files' | 'volumes';
    slug: string;
    disabled?: boolean;
    lock?: string | null;
    /** extra error messages (validator issues of the location) */
    error?: string[];
    /** show the live check errors (create forms: after a change / submit) */
    showErrors?: boolean;
    agentHome?: string;
    /** "Folder" / "File" in the resolved hint */
    noun?: string;
    testid?: string;
    onchange: (l: StorageLocation) => void;
  };
  let { value, kind, slug, disabled = false, lock = null, error = [], showErrors = true, agentHome, noun, testid = 'location', onchange }: Props = $props();
  let lastPath = $state('');
  const mode = $derived(value?.kind === 'path' ? 'path' : 'auto');
  const local = $derived.by(() => {
    const is = new Issues('template');
    checkLocation(value, kind, 'l', is);
    return is.list.filter((i) => i.severity === 'error').map((i) => i.message);
  });
  const errs = $derived(mode === 'path' && (showErrors || (value.kind === 'path' && value.path !== '')) ? [...new Set([...local, ...error])] : error);
  const resolved = $derived(agentHome ? locationPath(value, kind, slug || '<name>', agentHome) : null);
  function setMode(m: 'auto' | 'path') {
    if (m === mode) return;
    if (m === 'auto') {
      if (value.kind === 'path') lastPath = value.path;
      onchange({ kind: 'auto' });
    } else onchange({ kind: 'path', path: lastPath || `./${kind}/${slug || 'name'}` });
  }
</script>

<Field label="Location on the server" id="{testid}-path" group {lock} testid={testid}>
  <div class="stack tight">
    <SegmentedControl
      options={[
        { value: 'auto' as const, label: 'Auto (recommended)' },
        { value: 'path' as const, label: 'Custom path' },
      ]}
      value={mode}
      onchange={setMode}
      {disabled}
      ariaLabel="Location on the server"
      testid="{testid}-mode"
    />
    {#if mode === 'path' && value.kind === 'path'}
      <input id="{testid}-path" class="input mono" value={value.path} oninput={(e) => onchange({ kind: 'path', path: e.currentTarget.value.trim() })} placeholder="./{kind}/{slug || 'name'}" {disabled} spellcheck="false" autocomplete="off" aria-invalid={errs.length ? 'true' : undefined} data-testid="{testid}-path" />
    {/if}
  </div>
  {#each errs as e, k (k)}<div class="field-err" role="alert" data-testid="{testid}-err">{e}</div>{/each}
  {#if !errs.length}
    <div class="hint" data-testid="{testid}-hint">
      {#if mode === 'auto'}{locationLabel(value, kind, slug || '<slug>', agentHome)} — one copy per site{:else}"./" = the agent home ({agentHome ?? 'the agent data dir, default /var/lib/opm'}); or an absolute path like /srv/data{/if}{#if resolved && mode === 'path'}. {noun ?? (kind === 'files' ? 'File' : 'Folder')}: <code>{resolved}</code>{/if}
    </div>
  {/if}
</Field>
