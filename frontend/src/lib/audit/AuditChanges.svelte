<script lang="ts">
  import type { AuditChange } from '@opm/shared';
  import Icon from '$lib/ui/Icon.svelte';
  import { Lock, ArrowRight } from '$lib/ui/icons';
  import { auditValueText } from './audit';

  /** before → after diff of an audit entry (one row per changed path). */
  let { changes, total = changes.length }: { changes: AuditChange[]; total?: number } = $props();
</script>

{#if !changes.length}
  <p class="faint small" data-testid="audit-no-changes">No document changes recorded.</p>
{:else}
  <ul class="changes" data-testid="audit-changes">
    {#each changes as ch, i (ch.path + ':' + i)}
      {@const b = auditValueText(ch.before)}
      {@const a = auditValueText(ch.after)}
      <li class="ch" class:secret={ch.secret} data-testid="audit-change">
        <div class="path row">
          <code class="mono">{ch.path}</code>
          {#if ch.secret}<span class="badge amber" data-testid="audit-secret"><Icon icon={Lock} size={11} />secret changed</span>{/if}
        </div>
        <div class="vals">
          <div class="v before" class:empty={b.empty}>
            <pre class="mono">{b.text}</pre>
            {#if b.truncated !== null}<span class="xs faint">(truncated, {b.truncated} bytes)</span>{/if}
          </div>
          <span class="arrow" aria-hidden="true"><Icon icon={ArrowRight} size={13} /></span>
          <div class="v after" class:empty={a.empty}>
            <pre class="mono">{a.text}</pre>
            {#if a.truncated !== null}<span class="xs faint">(truncated, {a.truncated} bytes)</span>{/if}
          </div>
        </div>
      </li>
    {/each}
  </ul>
  {#if total > changes.length}<p class="small faint" style="margin-top:8px" data-testid="audit-changes-capped">Showing {changes.length} of {total} changes.</p>{/if}
{/if}

<style>
  .changes {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--sp-2);
  }
  .ch {
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    padding: var(--sp-2) var(--sp-3);
    background: var(--surface);
  }
  .path code {
    font-size: var(--fs-xs);
    color: var(--text);
    word-break: break-all;
  }
  .vals {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto minmax(0, 1fr);
    gap: var(--sp-2);
    align-items: start;
    margin-top: 6px;
  }
  .arrow {
    color: var(--text-3);
    padding-top: 6px;
  }
  .v {
    border-radius: var(--r-sm);
    padding: 4px 8px;
    min-width: 0;
  }
  .v pre {
    margin: 0;
    font-size: var(--fs-xs);
    white-space: pre-wrap;
    word-break: break-word;
    max-height: 240px;
    overflow: auto;
  }
  .before {
    background: var(--red-soft);
    color: var(--red-text);
  }
  .before pre {
    text-decoration: line-through;
    text-decoration-color: color-mix(in srgb, var(--red-text) 45%, transparent);
  }
  .after {
    background: var(--green-soft);
    color: var(--green-text);
  }
  .v.empty {
    background: var(--surface-2);
    color: var(--text-3);
  }
  .v.empty pre,
  .secret .before pre {
    text-decoration: none;
  }
  @media (max-width: 560px) {
    .vals {
      grid-template-columns: minmax(0, 1fr);
    }
    .arrow {
      display: none;
    }
  }
</style>
