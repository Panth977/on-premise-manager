<script lang="ts">
  /**
   * A dev-tool credential (password / token) — ONE control for cloud presets and agent connections (§21 #97 / #99 / #102 /
   * #103): None · Value (a static value typed here, `{ kind: 'literal' }`) · Secret (picker of the config's secrets +
   * optional "+ New secret…") · Container env (§21 #99: `{{ containers.<c>.env.<NAME> }}` of the target container) ·
   * Expression (advanced, field devtool).
   * Value (§21 #103): masked input with a show / hide toggle. `literalWhere` = 'config' (cloud presets: stored in plain
   * text in the config → non-blocking warning) | 'agent' (manual connections: stored on the agent, masked in every read).
   * A saved value that comes back as MASK (agent reads) shows "stored" + the `reveal` control (agent RevealValue) + Change.
   * Test ids: {testid} (data-mode) · {testid}-mode(-<m>) · {testid}-secret · {testid}-env · {testid}-env-note ·
   * {testid}-expr · {testid}-literal · {testid}-show · {testid}-change · {testid}-literal-warning · {testid}-literal-stored.
   */
  import type { Snippet } from 'svelte';
  import { MASK } from '@opm/shared/types';
  import type { ExprContext } from '@opm/shared/expr';
  import { DEVTOOL_LITERAL_WARNING, containerEnvExpr, containerEnvOfExpr, type DevToolCredential } from '@opm/shared/devtools';
  import SegmentedControl from '../../primitives/SegmentedControl.svelte';
  import Combobox from '../../primitives/Combobox.svelte';
  import ExprInput from '../../primitives/ExprInput.svelte';
  import Btn from '../../primitives/Btn.svelte';
  import Icon from '../../primitives/Icon.svelte';
  import { secretExpr, secretOfCredential, type EnvChoice } from './devtools';

  type Mode = 'none' | 'secret' | 'env' | 'expr' | 'literal';
  type Props = {
    value: DevToolCredential;
    onchange: (c: DevToolCredential) => void;
    secrets: { value: string; label?: string; hint?: string }[];
    ctx: ExprContext | null;
    disabled?: boolean;
    invalid?: boolean;
    /** "password" | "token" */
    what: string;
    testid: string;
    /** "+ New secret…" picked (cloud: opens the secret drawer) */
    onnewsecret?: () => void;
    /** the target container (null = not a container target → no "Container env" mode) */
    envContainer?: string | null;
    envChoices?: EnvChoice[];
    /** where a static Value is kept (§21 #103): 'config' = cloud preset (plain text in the config, warning) | 'agent' = manual connection */
    literalWhere?: 'config' | 'agent';
    /** a stored literal (MASK) of a saved connection / preset: render the reveal control (agent RevealValue) */
    reveal?: Snippet;
  };
  let { value, onchange, secrets, ctx, disabled = false, invalid = false, what, testid, onnewsecret, envContainer = null, envChoices = [], literalWhere = 'config', reveal }: Props = $props();

  const NEW = '__new__';
  const envRef = $derived(value.kind === 'expr' ? containerEnvOfExpr(value.expr) : null);
  const stored = $derived<Mode>(
    value.kind === 'none' ? 'none' : value.kind === 'literal' ? 'literal' : secretOfCredential(value) !== null || value.expr === '' ? 'secret' : envRef && (envContainer === envRef.containerSlug || !envContainer) ? 'env' : 'expr',
  );
  /** the mode the operator picked (an empty secret pick stays "secret"; an expression stays "expr" while typed) */
  let picked = $state<Mode | null>(null);
  const mode = $derived<Mode>(picked && !(picked === 'none' && stored !== 'none') && !(stored === 'literal' && picked !== 'literal') ? picked : stored);
  const secretSlug = $derived(secretOfCredential(value) ?? '');
  const exprText = $derived(value.kind === 'expr' ? value.expr : '');
  const opts = $derived([...secrets, ...(onnewsecret ? [{ value: NEW, label: '+ New secret…', hint: 'define it; set its value on Secrets' }] : [])]);
  const envSlug = $derived(envRef?.containerSlug ?? envContainer ?? '');
  const envOpts = $derived(envChoices.map((e) => ({ value: e.name, hint: e.secret ? `secret · ${e.detail}` : `plain · ${e.detail}` })));
  const pickedEnv = $derived(envChoices.find((e) => e.name === envRef?.name) ?? null);
  const showEnvMode = $derived(!!envContainer || stored === 'env');
  const masked = $derived(value.kind === 'literal' && value.value === MASK);
  let show = $state(false);
  /** previous credential per mode, so switching back restores it */
  let stash: Partial<Record<Mode, DevToolCredential>> = {};

  function setMode(m: Mode) {
    stash[mode] = value;
    picked = m;
    if (stash[m] && m !== 'secret' && m !== 'env') return onchange(stash[m]!);
    if (m === 'none') onchange({ kind: 'none' });
    else if (m === 'secret') onchange({ kind: 'expr', expr: secretSlug ? secretExpr(secretSlug) : '' });
    else if (m === 'env') {
      const first = envRef?.name ?? envChoices.find((e) => e.secret)?.name ?? null;
      onchange({ kind: 'expr', expr: first && envSlug ? containerEnvExpr(envSlug, first) : '' });
    } else if (m === 'literal') onchange({ kind: 'literal', value: '' });
    else onchange({ kind: 'expr', expr: value.kind === 'expr' ? value.expr : '' });
  }
  const modes = $derived([
    { value: 'none' as Mode, label: 'None', title: `No ${what}` },
    {
      value: 'literal' as Mode,
      label: 'Value',
      title: literalWhere === 'config' ? `Type the ${what}: stored in plain text in the config (use a secret for production)` : `Type the ${what}: stored on this site's agent (masked in every read)`,
    },
    { value: 'secret' as Mode, label: 'Secret', title: `The ${what} is a secret of this config — resolved on the site` },
    ...(showEnvMode ? [{ value: 'env' as Mode, label: 'Container env', title: `The ${what} is an env var of container ${envSlug} — resolved on the site` }] : []),
    { value: 'expr' as Mode, label: 'Expression', title: 'Advanced: any {{ }} expression (secret.*, var.*, site.*, self.*, deployment.*, containers.* incl. containers.<c>.env.<NAME>)' },
  ]);
</script>

<div class="cred" data-testid={testid} data-mode={mode}>
  <SegmentedControl options={modes} value={mode} onchange={setMode} size="sm" {disabled} ariaLabel="{what} source" testid="{testid}-mode" />
  {#if mode === 'secret'}
    <Combobox
      value={secretSlug}
      options={opts}
      mono
      placeholder="choose a secret"
      emptyText="No secrets in this config"
      {invalid}
      {disabled}
      ariaLabel="Secret for the {what}"
      testid="{testid}-secret"
      onchange={(v) => {
        if (v === NEW) onnewsecret?.();
        else if (v) onchange({ kind: 'expr', expr: secretExpr(v) });
      }}
    />
  {:else if mode === 'env'}
    <div class="envrow">
      <span class="hint mono">{envSlug ? `containers.${envSlug}.env.` : 'pick a container target'}</span>
      <Combobox value={envRef?.name ?? ''} options={envOpts} mono placeholder="choose an env var" emptyText={envSlug ? `Container ${envSlug} has no env vars` : 'The target is not a container'} {invalid} {disabled} ariaLabel="Env var of container {envSlug} for the {what}" testid="{testid}-env" onchange={(v) => envSlug && v && onchange({ kind: 'expr', expr: containerEnvExpr(envSlug, v) })} />
    </div>
    {#if pickedEnv}<div class="hint" data-testid="{testid}-env-note">→ {pickedEnv.detail}{pickedEnv.secret ? ' · the container gets it from a secret; resolved on the site' : ' (plain value)'}</div>{/if}
  {:else if mode === 'expr'}
    <ExprInput value={exprText} onchange={(t) => onchange({ kind: 'expr', expr: t })} field="devtool" {ctx} {disabled} {invalid} placeholder={'{{ secret.db-password }}'} ariaLabel="Expression for the {what}" testid="{testid}-expr" />
    <span class="hint">Resolved on the site at every connect (secret values never reach the browser).</span>
  {:else if mode === 'literal' && value.kind === 'literal'}
    {#if masked}
      <div class="row" data-testid="{testid}-literal-stored">
        <span class="mono stored">••••••</span>
        {#if reveal}{@render reveal()}{/if}
        {#if !disabled}<Btn size="sm" variant="ghost" icon="edit" onclick={() => onchange({ kind: 'literal', value: '' })} data-testid="{testid}-change">Change</Btn>{/if}
      </div>
      <span class="hint">{literalWhere === 'config' ? 'A static value of the cloud config (masked here; Reveal it below the form — logged, hides after 60 s).' : 'Stored on the agent (masked in every read). View is logged and hides after 60 s.'}</span>
    {:else}
      <div class="row">
        <input class="input mono grow" type={show ? 'text' : 'password'} value={value.value} oninput={(e) => onchange({ kind: 'literal', value: e.currentTarget.value })} autocomplete="new-password" spellcheck="false" placeholder="type the {what}" aria-label={what} aria-invalid={invalid ? 'true' : undefined} {disabled} data-testid="{testid}-literal" />
        <Btn size="sm" variant="ghost" icon={show ? 'eyeOff' : 'eye'} onclick={() => (show = !show)} aria-label={show ? `Hide the ${what}` : `Show the ${what}`} data-testid="{testid}-show" />
      </div>
      {#if literalWhere === 'config'}
        <div class="banner warn litwarn" role="note" data-testid="{testid}-literal-warning"><Icon name="alert" size={13} /> <span>{DEVTOOL_LITERAL_WARNING}</span></div>
      {:else}
        <span class="hint">Stored on this site's agent like a secret (never in the cloud config). Prefer a secret when the value already is one.</span>
      {/if}
    {/if}
  {/if}
</div>

<style>
  .cred {
    display: flex;
    flex-direction: column;
    gap: 6px;
    align-items: stretch;
    min-width: 0;
  }
  .cred > :global(:first-child) {
    align-self: flex-start;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .grow {
    flex: 1;
    min-width: 0;
  }
  .stored {
    letter-spacing: 1px;
    color: var(--text-2, inherit);
  }
  .litwarn {
    gap: 6px;
    margin: 0;
    padding: 6px 10px;
    font-size: var(--fs-sm);
  }
  .envrow {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    gap: 6px;
    align-items: center;
  }
  @media (max-width: 520px) {
    .envrow {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>
