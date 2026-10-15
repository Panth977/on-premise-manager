<script lang="ts">
  /**
   * Health check (SPEC §15.4) — ONE form for the cloud image page, the cloud container override and the agent container
   * override (§21 #102). Type none / http / tcp / exec / log, then the type's fields, then the timing fields.
   * Test ids: hc-type(-<t>) · hc-port · hc-path · hc-status · hc-cmd · hc-exit · hc-pattern · hc-interval · hc-timeout ·
   * hc-retries · hc-start.
   */
  import type { HealthCheck, ValidationIssue } from '@opm/shared/types';
  import Field from '../../primitives/Field.svelte';
  import ChipList from '../../primitives/ChipList.svelte';
  import SegmentedControl from '../../primitives/SegmentedControl.svelte';
  import NumberStepper from '../../primitives/NumberStepper.svelte';
  import Select from '../../primitives/Select.svelte';

  type Props = {
    value: HealthCheck | null;
    portNames: string[];
    /** issues of the health check (paths `${base}.<field>`) */
    issues?: readonly ValidationIssue[];
    base?: string;
    disabled?: boolean;
    idp?: string;
    onchange?: (v: HealthCheck | null) => void;
  };
  let { value = $bindable(null), portNames, issues = [], base = 'healthcheck', disabled = false, idp = 'hc', onchange }: Props = $props();
  type T = 'none' | HealthCheck['type'];
  const type = $derived<T>(value ? value.type : 'none');
  const at = (k: string) => issues.filter((i) => i.path === `${base}.${k}`);

  function set(v: HealthCheck | null) {
    value = v;
    onchange?.(v);
  }
  function setType(t: T) {
    if (t === 'none') return set(null);
    const common = value ? { intervalSec: value.intervalSec, timeoutSec: value.timeoutSec, retries: value.retries, startPeriodSec: value.startPeriodSec } : { intervalSec: 10, timeoutSec: 5, retries: 3, startPeriodSec: 30 };
    const port = portNames[0] ?? '';
    if (t === 'http') set({ ...common, type: 'http', portName: port, path: '/healthz', expectStatus: 200 });
    else if (t === 'tcp') set({ ...common, type: 'tcp', portName: port });
    else if (t === 'exec') set({ ...common, type: 'exec', command: [], expectExitCode: 0 });
    else set({ ...common, type: 'log', pattern: '' });
  }
  function patch(p: Record<string, unknown>) {
    if (!value) return;
    set({ ...$state.snapshot(value), ...p } as HealthCheck);
  }
  const TYPES = (['none', 'http', 'tcp', 'exec', 'log'] as T[]).map((t) => ({ value: t, label: t }));
</script>

<div class="hc stack" data-testid="hc">
  <Field label="Type" id="{idp}-type" group>
    <SegmentedControl id="{idp}-type" options={TYPES} value={type} onchange={setType} {disabled} ariaLabel="Health check type" testid="hc-type" />
  </Field>
  {#if value}
    {#if value.type === 'http' || value.type === 'tcp'}
      <div class="form-grid">
        <Field label="Port" id="{idp}-port" required issues={at('portName')}>
          <Select id="{idp}-port" value={value.portName} options={portNames.map((p) => ({ value: p, label: p }))} mono fallback={value.portName || undefined} placeholder={portNames.length ? '— choose a port —' : 'add a port first'} {disabled} ariaLabel="Port" testid="hc-port" onchange={(v) => patch({ portName: v })} />
        </Field>
        {#if value.type === 'http'}
          <Field label="Path" id="{idp}-path" required issues={at('path')}><input id="{idp}-path" class="input mono" value={value.path} oninput={(e) => patch({ path: e.currentTarget.value })} {disabled} data-testid="hc-path" /></Field>
          <Field label="Expect status" id="{idp}-st" issues={at('expectStatus')}><NumberStepper id="{idp}-st" min={100} max={599} value={value.expectStatus} onchange={(v) => patch({ expectStatus: v })} {disabled} label="Expect status" testid="hc-status" /></Field>
        {/if}
      </div>
    {:else if value.type === 'exec'}
      <div class="form-grid">
        <Field label="Command" id="{idp}-cmd" required group hint="One argument per chip (Enter after each)" issues={at('command')}><ChipList id="{idp}-cmd" value={value.command} mono placeholder="e.g. redis-cli" onchange={(v) => patch({ command: v })} {disabled} testid="hc-cmd" /></Field>
        <Field label="Expect exit code" id="{idp}-ec" issues={at('expectExitCode')}><NumberStepper id="{idp}-ec" min={0} max={255} value={value.expectExitCode} onchange={(v) => patch({ expectExitCode: v })} {disabled} label="Expect exit code" testid="hc-exit" /></Field>
      </div>
    {:else if value.type === 'log'}
      <Field label="Pattern (RE2)" id="{idp}-pat" required hint="Matched against stdout / stderr since start" issues={at('pattern')}><input id="{idp}-pat" class="input mono" value={value.pattern} oninput={(e) => patch({ pattern: e.currentTarget.value })} {disabled} placeholder="Ready to accept connections" data-testid="hc-pattern" /></Field>
    {/if}
    <div class="form-grid">
      <Field label="Interval s" id="{idp}-iv" hint="1–3600" issues={at('intervalSec')}><NumberStepper id="{idp}-iv" min={1} max={3600} value={value.intervalSec} onchange={(v) => patch({ intervalSec: v })} {disabled} label="Interval" testid="hc-interval" /></Field>
      <Field label="Timeout s" id="{idp}-to" hint="1–600" issues={at('timeoutSec')}><NumberStepper id="{idp}-to" min={1} max={600} value={value.timeoutSec} onchange={(v) => patch({ timeoutSec: v })} {disabled} label="Timeout" testid="hc-timeout" /></Field>
      <Field label="Retries" id="{idp}-re" hint="1–100" issues={at('retries')}><NumberStepper id="{idp}-re" min={1} max={100} value={value.retries} onchange={(v) => patch({ retries: v })} {disabled} label="Retries" testid="hc-retries" /></Field>
      <Field label="Start period s" id="{idp}-sp" hint="0–3600" issues={at('startPeriodSec')}><NumberStepper id="{idp}-sp" min={0} max={3600} value={value.startPeriodSec} onchange={(v) => patch({ startPeriodSec: v })} {disabled} label="Start period" testid="hc-start" /></Field>
    </div>
  {:else}
    <p class="hint">No health check: a service is “completed” after running 10 s without a restart.</p>
  {/if}
</div>
