<script lang="ts">
  import type { HealthCheck, ValidationIssue } from '@opm/shared';
  import Field from '../ui/Field.svelte';
  import StringListEditor from '../ui/StringListEditor.svelte';
  import SegmentedControl from '../ui/SegmentedControl.svelte';
  import NumberStepper from '../ui/NumberStepper.svelte';
  import Select from '../ui/Select.svelte';
  import { errorsAt } from '../util/issues';

  type Props = { value: HealthCheck | null; portNames: string[]; issues?: ValidationIssue[]; base?: string; disabled?: boolean; idp?: string };
  let { value = $bindable(null), portNames, issues = [], base = 'healthcheck', disabled = false, idp = 'hc' }: Props = $props();
  type T = 'none' | HealthCheck['type'];
  const type = $derived<T>(value ? value.type : 'none');
  const err = (k: string) => errorsAt(issues, `${base}.${k}`);

  function setType(t: T) {
    if (t === 'none') {
      value = null;
      return;
    }
    const common = value ? { intervalSec: value.intervalSec, timeoutSec: value.timeoutSec, retries: value.retries, startPeriodSec: value.startPeriodSec } : { intervalSec: 10, timeoutSec: 5, retries: 3, startPeriodSec: 30 };
    const port = portNames[0] ?? '';
    if (t === 'http') value = { ...common, type: 'http', portName: port, path: '/healthz', expectStatus: 200 };
    else if (t === 'tcp') value = { ...common, type: 'tcp', portName: port };
    else if (t === 'exec') value = { ...common, type: 'exec', command: [], expectExitCode: 0 };
    else value = { ...common, type: 'log', pattern: '' };
  }
  function num(k: string, v: number) {
    if (!value) return;
    (value as unknown as Record<string, unknown>)[k] = v;
  }
  const hv = $derived(value as (HealthCheck & Record<string, unknown>) | null);
</script>

<div class="hc stack">
  <Field label="Type" id="{idp}-type">
    <SegmentedControl options={['none', 'http', 'tcp', 'exec', 'log'] as T[]} value={type} onchange={(t) => setType(t)} {disabled} ariaLabel="Health check type" testid="hc-type" />
  </Field>
  {#if value && hv}
    {#if value.type === 'http' || value.type === 'tcp'}
      <div class="grid-3">
        <Field label="Port" id="{idp}-port" required error={err('portName')}>
          <Select
            id="{idp}-port"
            value={value.portName}
            options={portNames.map((p) => ({ value: p, label: p, mono: true }))}
            fallback={value.portName || null}
            placeholder={portNames.length ? '— choose a port —' : 'add a port first'}
            {disabled}
            ariaLabel="Port"
            testid="hc-port"
            onchange={(v) => value && (value.type === 'http' || value.type === 'tcp') && (value.portName = v)}
          />
        </Field>
        {#if value.type === 'http'}
          <Field label="Path" id="{idp}-path" required error={err('path')}><input id="{idp}-path" class="input mono" bind:value={value.path} {disabled} /></Field>
          <Field label="Expect status" id="{idp}-st" error={err('expectStatus')}><NumberStepper id="{idp}-st" min={100} max={599} value={value.expectStatus} onchange={(v) => num('expectStatus', v)} {disabled} /></Field>
        {/if}
      </div>
    {:else if value.type === 'exec'}
      <div class="grid-2">
        <Field label="Command" id="{idp}-cmd" required error={err('command')}><StringListEditor bind:value={value.command} nullable={false} placeholder="e.g. redis-cli ping" addLabel="Add argument" {disabled} /></Field>
        <Field label="Expect exit code" id="{idp}-ec" error={err('expectExitCode')}><NumberStepper id="{idp}-ec" min={0} max={255} value={value.expectExitCode} onchange={(v) => num('expectExitCode', v)} {disabled} /></Field>
      </div>
    {:else if value.type === 'log'}
      <Field label="Pattern (RE2)" id="{idp}-pat" required hint="Matched against stdout / stderr since start" error={err('pattern')}><input id="{idp}-pat" class="input mono" bind:value={value.pattern} {disabled} placeholder="Ready to accept connections" /></Field>
    {/if}
    <div class="grid-4">
      <Field label="Interval s" id="{idp}-iv" hint="1–3600" error={err('intervalSec')}><NumberStepper id="{idp}-iv" min={1} max={3600} value={hv.intervalSec as number} onchange={(v) => num('intervalSec', v)} {disabled} /></Field>
      <Field label="Timeout s" id="{idp}-to" hint="1–600" error={err('timeoutSec')}><NumberStepper id="{idp}-to" min={1} max={600} value={hv.timeoutSec as number} onchange={(v) => num('timeoutSec', v)} {disabled} /></Field>
      <Field label="Retries" id="{idp}-re" hint="1–100" error={err('retries')}><NumberStepper id="{idp}-re" min={1} max={100} value={hv.retries as number} onchange={(v) => num('retries', v)} {disabled} /></Field>
      <Field label="Start period s" id="{idp}-sp" hint="0–3600" error={err('startPeriodSec')}><NumberStepper id="{idp}-sp" min={0} max={3600} value={hv.startPeriodSec as number} onchange={(v) => num('startPeriodSec', v)} {disabled} /></Field>
    </div>
  {:else}
    <p class="small muted">No health check: a service is “completed” after running 10 s without a restart.</p>
  {/if}
</div>

