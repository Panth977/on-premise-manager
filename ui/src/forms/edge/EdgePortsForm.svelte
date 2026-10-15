<script lang="ts">
  /**
   * Edge ports & limits (SPEC §12; §21 #102) — inline card content, the same in the cloud Edge tab and the agent Edge page:
   * HTTP port · HTTPS port (toggle off = no TLS listener) · Max body size (nginx client_max_body_size).
   */
  import type { Edge, ValidationIssue } from '@opm/shared/types';
  import Field from '../../primitives/Field.svelte';
  import NumberStepper from '../../primitives/NumberStepper.svelte';
  import Toggle from '../../primitives/Toggle.svelte';
  import ByteSizeInput from './ByteSizeInput.svelte';
  interface Props {
    edge: Edge;
    disabled?: boolean;
    /** issues under `…config.edge` keyed by the field (httpPort / httpsPort / maxBodyBytes) */
    issues: (field: 'httpPort' | 'httpsPort' | 'maxBodyBytes') => readonly ValidationIssue[];
    lock?: string | null;
  }
  let { edge, disabled = false, issues, lock = null }: Props = $props();
</script>

<div class="form-grid ports" data-testid="edge-ports">
  <Field label="HTTP port" id="e-http" required {lock} issues={issues('httpPort')}>
    <NumberStepper id="e-http" value={Number.isNaN(edge.httpPort) ? null : edge.httpPort} onchange={(v) => (edge.httpPort = v ?? NaN)} min={1} max={65535} {disabled} label="HTTP port" testid="edge-http" />
  </Field>
  <Field label="HTTPS port" id="e-https" hint="Off = no TLS listener" {lock} issues={issues('httpsPort')}>
    <div class="row">
      <Toggle checked={edge.httpsPort !== null} onchange={(on) => (edge.httpsPort = on ? 443 : null)} {disabled} label={edge.httpsPort === null ? 'off' : ''} ariaLabel="HTTPS listener" testid="edge-https-on" />
      {#if edge.httpsPort !== null}<NumberStepper id="e-https" value={edge.httpsPort} onchange={(v) => (edge.httpsPort = v)} min={1} max={65535} {disabled} label="HTTPS port" testid="edge-https" />{/if}
    </div>
  </Field>
  <Field label="Max body size" id="e-mb" group hint="Request bodies above it get 413 (nginx client_max_body_size); a proxy route may override it" {lock} issues={issues('maxBodyBytes')}>
    <ByteSizeInput bind:value={edge.maxBodyBytes} kind="edge" {disabled} testid="edge-max-body" />
  </Field>
</div>

<style>
  .row {
    display: flex;
    gap: 8px;
    align-items: center;
  }
</style>
