<script lang="ts">
  import type { AuditLogEntry } from '@opm/shared';
  import { catalog } from '$lib/stores/catalog.svelte';
  import { fmtDate } from '$lib/util/format';
  import RelTime from '$lib/ui/RelTime.svelte';
  import IdChip from '$lib/ui/IdChip.svelte';
  import AuditChanges from './AuditChanges.svelte';
  import AuditOutcome from './AuditOutcome.svelte';
  import { auditActionLabel, auditActionTone, auditTargetHref, AUDIT_TARGET_TYPE_LABELS } from './audit';

  /** Every field of one audit entry (audit-log drawer). */
  let { entry: e }: { entry: AuditLogEntry } = $props();
  const href = $derived(auditTargetHref(e.target));
  const account = $derived(e.actor === 'system' ? null : catalog.accounts.find((a) => a.uid === e.actor));
  const dataJson = $derived(Object.keys(e.data).length ? JSON.stringify(e.data, null, 2) : '');
</script>

<div class="stack loose">
  <div class="row wrap">
    <span class="badge {auditActionTone(e.action)}" data-testid="audit-drawer-action">{e.action}</span> <span class="small muted" data-testid="audit-drawer-action-label">{auditActionLabel(e.action)}</span>
    <AuditOutcome entry={e} />
    <span class="small muted"><RelTime ts={e.ts} /> · {fmtDate(e.ts)}</span>
  </div>

  {#if e.outcome === 'error' && e.errorMessage}
    <div class="banner error small" data-testid="audit-error"><div><b>{e.errorCode ?? 'error'}</b> · {e.errorMessage}</div></div>
  {/if}

  <dl class="kv">
    <dt>Actor</dt>
    <dd>
      {#if e.actor === 'system'}<span class="badge outline">system</span>
      {:else}
        {#if account}<a href="/app/accounts/{e.actor}">{account.name}</a>{/if}
        {#if e.actorEmail}<span class="muted">{account ? ' · ' : ''}{e.actorEmail}</span>{/if}
        <div><IdChip id={e.actor} /></div>
      {/if}
    </dd>
    <dt>Target</dt>
    <dd>
      <span class="faint small">{AUDIT_TARGET_TYPE_LABELS[e.targetType] ?? e.targetType}</span>
      {#if href}<a class="mono small" {href} data-testid="audit-target-link">{e.target}</a>{:else}<code class="mono small">{e.target}</code>{/if}
    </dd>
    <dt>Operation</dt>
    <dd><code class="mono small">{e.op || '—'}</code></dd>
    <dt>Status</dt>
    <dd class="small">{e.status}{#if e.errorCode} · <code class="mono">{e.errorCode}</code>{/if}</dd>
    <dt>Request id</dt>
    <dd>{#if e.requestId}<IdChip id={e.requestId} />{:else}<span class="faint">—</span>{/if}</dd>
    <dt>IP</dt>
    <dd class="mono small">{e.ip ?? '—'}</dd>
    <dt>User agent</dt>
    <dd class="small muted ua">{e.userAgent ?? '—'}</dd>
    <dt>Audit id</dt>
    <dd><IdChip id={e.auditId} /></dd>
  </dl>

  <section>
    <h4 class="h">Changes {#if e.changesTotal}<span class="faint small">({e.changesTotal})</span>{/if}</h4>
    <AuditChanges changes={e.changes} total={e.changesTotal} />
  </section>

  {#if dataJson}
    <section>
      <h4 class="h">Data</h4>
      <pre class="code wrap" data-testid="audit-data">{dataJson}</pre>
    </section>
  {/if}
</div>

<style>
  .h {
    font-size: var(--fs-sm);
    font-weight: var(--fw-semibold);
    margin: 0 0 var(--sp-2);
  }
  .ua {
    word-break: break-word;
  }
</style>
