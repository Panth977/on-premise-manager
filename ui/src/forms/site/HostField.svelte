<script lang="ts">
  /**
   * Site host (SPEC §21 #51) — ONE control for the cloud (site settings, Add site) and the agent (§21 #102): optional.
   * "Auto-detect (recommended)" ON (value null) = the agent's detected / advertised address; OFF = an IP, a DNS name or
   * {{ var.… }}. A host expression without a value falls back to auto: V_HOST_AUTO shows as a note with a one-click
   * "Use auto-detect" (never blocking). Test ids: {testid}-field · {testid}-auto(-input) · {testid}-auto-info ·
   * {testid}-detected · {testid}(-input) · {testid}-fallback · use-auto-host.
   */
  import type { ExprContext } from '@opm/shared/expr';
  import type { SiteHost } from '@opm/shared/validate';
  import ExprInput from '../../primitives/ExprInput.svelte';
  import Toggle from '../../primitives/Toggle.svelte';
  import ValueText from '../../primitives/ValueText.svelte';
  import Icon from '../../primitives/Icon.svelte';
  import Btn from '../../primitives/Btn.svelte';

  type Props = {
    value: string | null;
    ctx: ExprContext | null;
    /** resolved host of this site; null while unknown */
    resolved?: SiteHost | null;
    /** the detected / advertised address of the enrolled site */
    detected?: string | null;
    errors?: string[];
    /** V_HOST_AUTO message (from validation or resolved.fallback) */
    fallback?: string | null;
    disabled?: boolean;
    id?: string;
    testid?: string;
    placeholder?: string;
  };
  let { value = $bindable(null), ctx, resolved = null, detected = null, errors = [], fallback = null, disabled = false, id, testid = 'site-host', placeholder = '10.0.0.11, app.example.com or {{ var.app-host }}' }: Props = $props();
  /** the user switched Auto-detect off: the input shows even while the value is still empty */
  // svelte-ignore state_referenced_locally
  let manual = $state(value !== null);
  const auto = $derived(value === null && !manual);
  let text = $state('');
  $effect(() => {
    if (value !== null && value !== text) text = value;
  });
  function setAuto(on: boolean) {
    manual = !on;
    if (on) value = null;
    else value = text.trim() === '' ? null : text;
  }
  const fb = $derived(fallback ?? resolved?.fallback?.message ?? null);
</script>

<div class="hf" data-testid="{testid}-field">
  <Toggle checked={auto} onchange={setAuto} {disabled} label="Auto-detect (recommended)" testid="{testid}-auto" />
  {#if auto}
    <div class="small muted info" data-testid="{testid}-auto-info">
      <Icon name="info" size={13} />
      {#if detected}
        <span>Detected address: <code class="mono" data-testid="{testid}-detected">{detected}</code></span>
      {:else}
        <span><span class="auto-pill">auto · detected on install</span> The agent reports the IP of this machine when it is installed; you never need to know it ahead of time.</span>
      {/if}
    </div>
  {:else}
    <ExprInput {id} value={text} oninput={(v) => ((text = v), (value = v.trim() === '' ? null : v))} field="host" {ctx} {disabled} invalid={errors.length > 0} {placeholder} {testid} ariaLabel="Host" />
    {#each errors as e, i (i)}<div class="field-err" role="alert">{e}</div>{/each}
    {#if fb}
      <div class="banner warn small fbk" data-testid="{testid}-fallback">
        <Icon name="alert" size={14} />
        <div class="grow">{fb}{#if resolved?.host && !resolved.host.startsWith('<auto:')} → <ValueText text={resolved.host} code />{/if}</div>
        {#if !disabled}<Btn size="sm" onclick={() => setAuto(true)} data-testid="use-auto-host">Use auto-detect</Btn>{/if}
      </div>
    {:else if !text.trim()}
      <div class="hint">Empty = auto-detect.</div>
    {/if}
  {/if}
</div>

<style>
  .hf {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .info {
    display: flex;
    gap: 6px;
    align-items: baseline;
    line-height: 1.5;
  }
  .auto-pill {
    display: inline-block;
    padding: 0 7px;
    border-radius: 999px;
    font-size: 11px;
    background: var(--accent-soft);
    color: var(--accent-text);
    margin-right: 4px;
  }
  .fbk {
    align-items: center;
  }
</style>
