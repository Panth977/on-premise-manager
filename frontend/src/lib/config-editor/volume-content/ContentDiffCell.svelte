<script lang="ts">
  /**
   * §21 #101 Diff page: one per-file content change of a volume (DiffChange.blob) → "View diff" opens a modal with the
   * text diff (TextDiff, round L) of the two blobs (GET /v1/blobs/{sha}); binary / > 2 MiB → sizes + hashes.
   */
  import type { DiffChange } from '@opm/shared';
  import { VOLUME_CONTENT_LIMITS, formatContentBytes, looksLikeText } from '@opm/shared';
  import Modal from '../../ui/Modal.svelte';
  import Button from '../../ui/Button.svelte';
  import TextDiff from '../../ui/TextDiff.svelte';
  import Spinner from '../../ui/Spinner.svelte';
  import Icon from '../../ui/Icon.svelte';
  import { GitCompare } from '../../ui/icons';
  import { blobBytes, decodeText } from './content';

  let { change }: { change: DiffChange } = $props();
  const blob = $derived(change.blob!);
  let open = $state(false);
  let res = $state<{ a: string; b: string } | { note: string } | null>(null);
  const sizeOf = (v: unknown) => (v && typeof v === 'object' && 'size' in v && typeof (v as { size: unknown }).size === 'number' ? (v as { size: number }).size : null);

  async function side(sha: string | null, size: number | null): Promise<string | { note: string }> {
    if (!sha) return '';
    if (size !== null && size > VOLUME_CONTENT_LIMITS.editorTextMaxBytes) return { note: `${formatContentBytes(size)} — too large for a text diff` };
    const b = await blobBytes(sha);
    return looksLikeText(b) ? decodeText(b) : { note: 'binary content' };
  }
  async function show() {
    open = true;
    if (res) return;
    try {
      const [a, b] = await Promise.all([side(blob.before, sizeOf(change.before)), side(blob.after, sizeOf(change.after))]);
      res = typeof a === 'string' && typeof b === 'string' ? { a, b } : { note: typeof a !== 'string' ? a.note : (b as { note: string }).note };
    } catch (e) {
      res = { note: `could not load the content: ${e instanceof Error ? e.message : String(e)}` };
    }
  }
</script>

<div><Button size="sm" variant="ghost" icon={GitCompare} onclick={show} data-testid="diff-blob-view">View diff</Button></div>

<Modal bind:open title="{blob.volumeSlug}/{blob.path || '(the file)'}" subtitle="Starter content of volume {blob.volumeSlug}: head → draft" size="lg" testid="diff-blob-modal">
  {#if !res}
    <div class="small faint"><Spinner size={14} /> Loading both versions…</div>
  {:else if 'note' in res}
    <div class="small muted" data-testid="diff-blob-note">{res.note} · {blob.before?.slice(0, 19) ?? '—'}… → {blob.after?.slice(0, 19) ?? '—'}…</div>
  {:else}
    <TextDiff a={res.a} b={res.b} aLabel={blob.before ? 'head' : '(new file)'} bLabel={blob.after ? 'draft' : '(removed)'} testid="diff-blob-text" />
  {/if}
  <div class="xs faint mono hashes"><Icon icon={GitCompare} size={11} /> {blob.before ?? 'none'} → {blob.after ?? 'none'}</div>
  {#snippet footer()}<Button onclick={() => (open = false)}>Close</Button>{/snippet}
</Modal>

<style>
  .hashes {
    margin-top: 8px;
    overflow-wrap: anywhere;
  }
</style>
