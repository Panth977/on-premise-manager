<script lang="ts">
  import { DEPLOY_PLACEHOLDER } from '@opm/shared';
  import { autoSegments } from '../util/autohost';
  /**
   * A resolved value; `<auto:slug>` placeholders render as "auto · slug" chips (host detected on install) and the
   * round-E DEPLOY_PLACEHOLDER (a required variable / secret without a template value) as a "set at deployment" token.
   */
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

{#each segs as s, i (i)}{#if s.deploy}<span class="deploy-chip" data-tip="A required variable / secret without a template value: typed when the deployment is created" data-testid="deploy-chip">set at deployment</span>{:else if s.auto}<span class="auto-chip" data-tip="Host auto-detected on install of site {s.auto}" data-testid="auto-chip">auto · {s.auto}</span>{:else if code}<code class="inline">{s.text}</code>{:else}{s.text}{/if}{/each}

<style>
  .deploy-chip {
    display: inline-flex;
    align-items: center;
    padding: 0 6px;
    border-radius: 999px;
    font-size: 11px;
    font-family: var(--font-sans);
    line-height: 17px;
    background: var(--violet-soft);
    color: var(--violet-text);
    border: 1px dashed color-mix(in srgb, var(--violet) 45%, transparent);
    white-space: nowrap;
    vertical-align: baseline;
  }
  .auto-chip {
    display: inline-flex;
    align-items: center;
    padding: 0 6px;
    border-radius: 999px;
    font-size: 11px;
    font-family: var(--font-sans);
    line-height: 17px;
    background: var(--accent-soft);
    color: var(--accent-text);
    border: 1px solid color-mix(in srgb, var(--accent) 35%, transparent);
    white-space: nowrap;
    vertical-align: baseline;
  }
</style>
