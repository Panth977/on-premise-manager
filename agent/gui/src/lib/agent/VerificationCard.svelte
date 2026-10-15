<script lang="ts">
  /** Result of a release check / the verify phase (opm-agent verify-release parity). */
  import type { ReleaseVerification } from '@opm/shared/types';
  import Icon from '$lib/ui/Icon.svelte';
  import CopyId from '$lib/ui/CopyId.svelte';
  import Time from '$lib/ui/Time.svelte';

  let { v, compact = false }: { v: ReleaseVerification; compact?: boolean } = $props();
  const SRC = { 'agent.json': 'agent.json releasePublicKey', cloud: 'the cloud (no releasePublicKey in agent.json)', flag: '--public-key flag' } as const;
  const tri = (b: boolean | null) => (b === null ? 'pending' : b ? 'ok' : 'bad');
</script>

<div class="ver" class:compact data-testid="verification">
  <div class="row between hd">
    <strong class="row tight">
      {#if v.signatureOk && v.sha256Ok !== false && v.selfTestOk !== false}<span class="ok-text row tight"><Icon name="shield" size={15} />Release {v.version} verified</span>
      {:else}<span class="bad-text row tight"><Icon name="alert" size={15} />Release {v.version} NOT verified</span>{/if}
    </strong>
    <span class="faint small">checked <Time ts={v.checkedAt} /></span>
  </div>
  <dl class="kv small">
    <dt>Signature</dt>
    <dd data-testid="ver-signature"><span class="badge {v.signatureOk ? 'ok' : 'bad'}">{v.signatureOk ? 'valid' : 'invalid'}</span> <span class="muted">Ed25519 over the sha256</span></dd>
    <dt>Key source</dt>
    <dd>{SRC[v.publicKeySource] ?? v.publicKeySource}</dd>
    <dt>Key fingerprint</dt>
    <dd><CopyId value={v.publicKeyFingerprint} label="key fingerprint" /></dd>
    <dt>sha256</dt>
    <dd><CopyId value={v.sha256} short={compact ? 16 : 24} label="sha256" /></dd>
    <dt>File sha256</dt>
    <dd data-testid="ver-sha">
      {#if tri(v.sha256Ok) === 'pending'}<span class="faint">checked after the download (during the upgrade)</span>
      {:else}<span class="badge {tri(v.sha256Ok)}">{v.sha256Ok ? 'matches' : 'mismatch'}</span>{/if}
    </dd>
    <dt>Self-test</dt>
    <dd data-testid="ver-selftest">
      {#if tri(v.selfTestOk) === 'pending'}<span class="faint"><code>opm-agent.new version</code> runs during the upgrade</span>
      {:else}<span class="badge {tri(v.selfTestOk)}">{v.selfTestOk ? `prints ${v.version}` : 'failed'}</span>{/if}
    </dd>
  </dl>
  {#if v.error}<div class="banner bad small"><Icon name="alert" /><div class="grow">{v.error}</div></div>{/if}
</div>

<style>
  .ver {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 12px 14px;
    border: 1px solid var(--border);
    border-radius: var(--r-lg);
    background: var(--surface-2);
    min-width: 0;
  }
  .row.tight {
    gap: 6px;
    flex-wrap: nowrap;
  }
  .hd {
    gap: 6px 12px;
  }
  .kv {
    gap: 5px 14px;
  }
</style>
