<script lang="ts" module>
  export const UNITS = [
    { value: 'B', mult: 1 },
    { value: 'KiB', mult: 1024 },
    { value: 'MiB', mult: 1024 * 1024 },
    { value: 'GiB', mult: 1024 * 1024 * 1024 },
  ] as const;
  export type Unit = (typeof UNITS)[number]['value'];
  /** largest unit that divides n exactly (100 MiB → [100, 'MiB']) */
  export function splitBytes(n: number): [number, Unit] {
    for (let i = UNITS.length - 1; i > 0; i--) if (n % UNITS[i]!.mult === 0) return [n / UNITS[i]!.mult, UNITS[i]!.value];
    return [n, 'B'];
  }
</script>

<script lang="ts">
  /**
   * Body size limit (bytes) with units — one control for both GUIs (§21 #102). kind "edge": null = unlimited | size. kind "route": null = inherit the edge,
   * 0 = unlimited, or a size (nginx client_max_body_size). Test ids: {testid}-mode-<inherit|unlimited|custom>,
   * {testid}-num, {testid}-unit.
   */
  import SegmentedControl from '../../primitives/SegmentedControl.svelte';
  import NumberStepper from '../../primitives/NumberStepper.svelte';
  import Select from '../../primitives/Select.svelte';

  type Props = { value: number | null; kind: 'edge' | 'route'; inheritText?: string; disabled?: boolean; testid?: string; onchange?: (v: number | null) => void };
  let { value = $bindable(null), kind, inheritText = '', disabled = false, testid = 'body-size', onchange }: Props = $props();
  type Mode = 'inherit' | 'unlimited' | 'custom';
  const modeOf = (v: number | null): Mode => (v === null ? (kind === 'edge' ? 'unlimited' : 'inherit') : v === 0 && kind === 'route' ? 'unlimited' : 'custom');
  // svelte-ignore state_referenced_locally
  let mode = $state<Mode>(modeOf(value));
  // svelte-ignore state_referenced_locally
  const init = value && value > 0 ? splitBytes(value) : ([100, 'MiB'] as [number, Unit]);
  let num = $state<number | null>(init[0]);
  let unit = $state<Unit>(init[1]);
  // follow outside changes (presets)
  $effect(() => {
    const v = value;
    if (modeOf(v) !== mode || (mode === 'custom' && v !== compute())) {
      mode = modeOf(v);
      if (v && v > 0) [num, unit] = splitBytes(v);
    }
  });
  function compute(): number | null {
    if (mode === 'inherit') return null;
    if (mode === 'unlimited') return kind === 'edge' ? null : 0;
    const m = UNITS.find((u) => u.value === unit)!.mult;
    return num !== null && Number.isFinite(num) ? Math.round(num * m) : NaN;
  }
  function push() {
    const v = compute();
    if (v === value) return;
    value = v;
    onchange?.(v);
  }
  const modes = $derived(
    kind === 'edge'
      ? [
          { value: 'unlimited' as Mode, label: 'Unlimited' },
          { value: 'custom' as Mode, label: 'Limit' },
        ]
      : [
          { value: 'inherit' as Mode, label: 'Inherit', title: inheritText ? `From the edge: ${inheritText}` : 'The edge limit' },
          { value: 'unlimited' as Mode, label: 'Unlimited' },
          { value: 'custom' as Mode, label: 'Limit' },
        ],
  );
</script>

<div class="size" data-testid={testid}>
  <SegmentedControl options={modes} bind:value={mode} size="sm" {disabled} ariaLabel="Body size limit" testid="{testid}-mode" onchange={push} />
  {#if mode === 'custom'}
    <span class="num"><NumberStepper bind:value={num} min={1} max={1048576} size="sm" {disabled} label="Size" testid="{testid}-num" onchange={push} /></span>
    <span class="unit"><Select value={unit} options={UNITS.map((u) => ({ value: u.value, label: u.value }))} size="sm" {disabled} ariaLabel="Unit" testid="{testid}-unit" onchange={(v) => ((unit = v as Unit), push())} /></span>
  {:else if mode === 'inherit' && inheritText}
    <span class="hint">edge: {inheritText}</span>
  {/if}
</div>

<style>
  .size {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
  }
  .num {
    width: 130px;
  }
  .unit {
    width: 92px;
  }
</style>
