<script lang="ts">
  /**
   * HTTP environments (§21 #97, Postman-like): `{{name}}` variables per environment; secret variables are stored on the
   * agent like literal passwords (masked in every read, RevealValue 60 s, logged); MASK on save keeps the stored value.
   */
  import { MASK } from '@opm/shared/types';
  import type { DevToolEnvironment } from '@opm/shared/devtools';
  import { errMessage } from '$lib/api';
  import { toasts } from '$lib/state/toast.svelte';
  import Drawer from '$lib/ui/Drawer.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Confirm from '$lib/ui/Confirm.svelte';
  import RevealValue from '$lib/ui/RevealValue.svelte';
  import { dev } from '../api';

  interface Props {
    open: boolean;
    site: string;
    envs: DevToolEnvironment[];
    activeId: string | null;
    onchanged: (activeId?: string | null) => void;
  }
  let { open = $bindable(false), site, envs, activeId, onchanged }: Props = $props();
  let cur = $state<string | null>(null);
  type Var = { name: string; value: string; secret: boolean; enabled: boolean; stored: boolean };
  let name = $state('');
  let vars = $state<Var[]>([]);
  let busy = $state(false);
  let err = $state<string | null>(null);
  let delOpen = $state(false);
  function pick(id: string | null) {
    cur = id;
    const e = envs.find((x) => x.id === id);
    name = e?.name ?? '';
    vars = e ? e.variables.map((v) => ({ name: v.name, value: String(v.value), secret: v.secret, enabled: v.enabled, stored: v.secret && v.value === MASK })) : [{ name: '', value: '', secret: false, enabled: true, stored: false }];
    err = null;
  }
  $effect(() => {
    if (open) pick(activeId ?? envs[0]?.id ?? null);
  });
  async function save() {
    if (!name.trim()) return (err = 'Name required.');
    const names = vars.filter((v) => v.name.trim()).map((v) => v.name.trim());
    if (new Set(names).size !== names.length) return (err = 'Variable names must be unique.');
    busy = true;
    try {
      const body = { name: name.trim(), variables: vars.filter((v) => v.name.trim()).map((v) => ({ name: v.name.trim(), value: v.stored ? MASK : v.value, secret: v.secret, enabled: v.enabled })) };
      const e = cur ? await dev.updateEnvironment({ siteSlug: site, environmentId: cur, body }) : await dev.createEnvironment({ siteSlug: site, body });
      toasts.ok(`Saved environment ${e.name}`);
      cur = e.id;
      onchanged(e.id);
    } catch (e) {
      err = errMessage(e);
    } finally {
      busy = false;
    }
  }
  async function remove() {
    if (!cur) return;
    try {
      await dev.deleteEnvironment({ siteSlug: site, environmentId: cur });
      toasts.ok('Environment deleted');
      onchanged(activeId === cur ? null : activeId);
      pick(null);
    } catch (e) {
      toasts.err(e, 'Delete failed');
    }
  }
</script>

<Drawer bind:open title="Environments" subtitle="Variables used as {'{{name}}'} in URLs, headers and bodies. Stored on the agent of {site}; shared by its operators." wide testid="env-drawer">
  <div class="ed">
    <nav class="envs" aria-label="Environments">
      {#each envs as e (e.id)}
        <button class:on={cur === e.id} onclick={() => pick(e.id)} data-testid="env-{e.name}"><Icon name="sliders" size={13} />{e.name}<span class="faint xs">{e.variables.length}</span></button>
      {/each}
      <button class:on={cur === null} onclick={() => pick(null)} data-testid="env-new"><Icon name="plus" size={13} />New environment</button>
    </nav>
    <div class="form stack">
      <div class="field">
        <label for="env-name">Name</label>
        <input id="env-name" class="input" bind:value={name} data-testid="env-name" />
      </div>
      <div class="vars">
        <div class="vh"><span>On</span><span>Variable</span><span>Value</span><span>Secret</span><span></span></div>
        {#each vars as v, i (i)}
          <div class="vr" data-testid="env-var-{i}">
            <input type="checkbox" bind:checked={v.enabled} aria-label="Enabled" />
            <input class="input sm mono" bind:value={v.name} placeholder="name" aria-label="Variable name" data-testid="env-var-name-{i}" />
            {#if v.stored}
              <div class="row gap-s">
                {#if cur}<RevealValue reveal={() => dev.revealEnvironmentValue({ siteSlug: site, environmentId: cur!, query: { name: v.name } })} label="{v.name} of {name}" testid="env-reveal-{i}" />{/if}
                <button class="btn ghost xs" onclick={() => ((v.stored = false), (v.value = ''))}>Change</button>
              </div>
            {:else}
              <input class="input sm mono" type={v.secret ? 'password' : 'text'} bind:value={v.value} placeholder="value" aria-label="Value" autocomplete="off" data-testid="env-var-value-{i}" />
            {/if}
            <input type="checkbox" bind:checked={v.secret} aria-label="Secret" title="Secret: stored masked, revealed only with View (logged)" data-testid="env-var-secret-{i}" />
            <button class="btn ghost icon xs" aria-label="Remove variable" onclick={() => (vars = vars.filter((_, j) => j !== i))}><Icon name="x" size={12} /></button>
          </div>
        {/each}
        <button class="btn ghost sm add" onclick={() => (vars = [...vars, { name: '', value: '', secret: false, enabled: true, stored: false }])} data-testid="env-var-add"><Icon name="plus" size={13} />Add variable</button>
      </div>
      <p class="hint">Use as <span class="mono">{'{{userId}}'}</span>. OPM expressions such as <span class="mono">{'{{ secret.api-token }}'}</span> are resolved on the site, not here.</p>
      {#if err}<span class="field-err">{err}</span>{/if}
    </div>
  </div>
  {#snippet footer()}
    {#if cur}<Btn variant="ghost" icon="trash" onclick={() => (delOpen = true)}>Delete</Btn>{/if}
    <span class="grow"></span>
    <Btn variant="ghost" onclick={() => (open = false)}>Close</Btn>
    <Btn variant="primary" {busy} onclick={save} data-testid="env-save">{cur ? 'Save' : 'Create'}</Btn>
  {/snippet}
</Drawer>
<Confirm bind:open={delOpen} title="Delete environment {name}?" confirmLabel="Delete" danger onconfirm={remove} />

<style>
  .ed {
    display: grid;
    grid-template-columns: 180px minmax(0, 1fr);
    gap: 16px;
  }
  .envs {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .envs button {
    all: unset;
    display: flex;
    gap: 6px;
    align-items: center;
    padding: 6px 8px;
    border-radius: var(--r-sm);
    cursor: pointer;
    font-size: var(--fs-sm);
  }
  .envs button .xs {
    margin-left: auto;
  }
  .envs button:hover {
    background: var(--surface-hover);
  }
  .envs button.on {
    background: var(--accent-soft);
    color: var(--accent-text);
  }
  .vars {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .vh,
  .vr {
    display: grid;
    grid-template-columns: 26px minmax(100px, 0.8fr) minmax(140px, 1.4fr) 48px 28px;
    gap: 6px;
    align-items: center;
  }
  .vh {
    font-size: var(--fs-xs);
    color: var(--text-3);
  }
  .add {
    align-self: flex-start;
  }
  @media (max-width: 700px) {
    .ed {
      grid-template-columns: 1fr;
    }
  }
</style>
