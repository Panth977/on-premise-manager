<script lang="ts">
  /**
   * A resolved value as text: `<auto:slug>` placeholders render as "auto · slug" chips (host detected on install) and the
   * DEPLOY_PLACEHOLDER of a required variable / secret without a value as a dashed "set at deployment" token (§17.0).
   * One component for both GUIs (was AutoHostText in the cloud, Deferred in the agent).
   */
  import { DEPLOY_PLACEHOLDER } from '@opm/shared/expr';
  import { autoSegments } from '../util/autohost';
  let { text, code = false }: { text: string; code?: boolean } = $props();
  type Seg = { text: string; auto: string | null; deploy: boolean };
  const segs = $derived<Seg[]>(
    autoSegments(text).flatMap((s): Seg[] => {
      if (s.auto) return [{ text: s.text, auto: s.auto, deploy: false }];
      const parts = s.text.split(DEPLOY_PLACEHOLDER);
      return parts.flatMap((p, i) => [...(p || parts.length === 1 ? [{ text: p, auto: null, deploy: false }] : []), ...(i < parts.length - 1 ? [{ text: DEPLOY_PLACEHOLDER, auto: null, deploy: true }] : [])]);
    }),
  );
</script>

{#each segs as s, i (i)}{#if s.deploy}<span class="deferred" title="A required variable / secret without a value: known only when the deployment is set up" data-testid="deferred">set at deployment</span>{:else if s.auto}<span class="auto-chip" title="Host auto-detected on install of site {s.auto}" data-testid="auto-chip">auto · {s.auto}</span>{:else if code}<code class="inline">{s.text}</code>{:else}{s.text}{/if}{/each}

<style>
  .deferred,
  .auto-chip {
    display: inline-flex;
    align-items: center;
    padding: 0 6px;
    border-radius: 999px;
    font-size: 11px;
    font-family: var(--font-sans);
    font-style: normal;
    line-height: 17px;
    white-space: nowrap;
    vertical-align: baseline;
  }
  .deferred {
    background: var(--blue-soft);
    color: var(--blue-text);
    border: 1px dashed color-mix(in srgb, var(--blue) 55%, transparent);
  }
  .auto-chip {
    background: var(--accent-soft);
    color: var(--accent-text);
    border: 1px solid color-mix(in srgb, var(--accent) 35%, transparent);
  }
</style>
