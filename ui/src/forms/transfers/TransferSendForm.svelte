<script lang="ts" module>
  import { TRANSFER_MAX } from '@opm/shared/types';
  export const TRANSFER_NOTE_MAX = 500;
  /** "1.2 MiB" */
  export function fmtBytes(n: number): string {
    if (n < 1024) return `${n} B`;
    const u = ['KiB', 'MiB', 'GiB', 'TiB'];
    let v = n / 1024;
    let i = 0;
    while (v >= 1024 && i < u.length - 1) (v /= 1024), i++;
    return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${u[i]}`;
  }
  /** the file checks of a transfer send (SPEC §21 #100) — the same in both GUIs */
  export function transferFileError(file: File | null): string | null {
    if (!file) return 'Choose a file';
    if (file.size > TRANSFER_MAX) return `At most 2 GiB (this file is ${fmtBytes(file.size)})`;
    if (/[/\\\0]/.test(file.name) || file.name.length > 255) return 'The file name is not allowed';
    return null;
  }
  export function transferNoteError(note: string): string | null {
    return note.length > TRANSFER_NOTE_MAX ? `At most ${TRANSFER_NOTE_MAX} characters (${note.length})` : null;
  }
</script>

<script lang="ts">
  /**
   * Send a file (Transfers, SPEC §21 #100 / #102) — ONE form for the cloud "Send a file to <site>" modal and the agent
   * "Send from the browser" / "Send from the server" modals: File (browser: file input; server path: the picked file,
   * rendered by the `picked` snippet) · Note (textarea, n / 500). Checks show inline after the first submit.
   * Test ids: send-file-input · send-file-info · send-note.
   */
  import type { Snippet } from 'svelte';
  import Field from '../../primitives/Field.svelte';
  interface Props {
    /** browser: a file from this computer; path: a file already picked on the server */
    source?: 'browser' | 'path';
    file?: File | null;
    note: string;
    submitted?: boolean;
    fileHint?: string;
    noteHint?: string;
    notePlaceholder?: string;
    /** path source: the picked file card */
    picked?: Snippet;
    /** a server error (e.g. "no such file") shown under the file */
    serverError?: string | null;
    disabled?: boolean;
  }
  let { source = 'browser', file = $bindable(null), note = $bindable(''), submitted = false, fileHint = 'Up to 2 GiB.', noteHint = 'Optional, shown to the person on the other end', notePlaceholder = '', picked, serverError = null, disabled = false }: Props = $props();
  const fileErr = $derived(source === 'browser' ? transferFileError(file) : null);
  const noteErr = $derived(transferNoteError(note));
</script>

<div class="stack" data-testid="transfer-send-form">
  {#if source === 'browser'}
    <Field label="File" id="ts-file" required hint={fileHint} error={[...(submitted && fileErr ? [fileErr] : []), ...(serverError ? [serverError] : [])]}>
      <input id="ts-file" class="input" type="file" {disabled} onchange={(e) => (file = e.currentTarget.files?.[0] ?? null)} data-testid="send-file-input" />
    </Field>
    {#if file}<div class="small muted" data-testid="send-file-info">{file.name} · {fmtBytes(file.size)}</div>{/if}
  {:else}
    <Field label="File" group required error={serverError}>
      {#if picked}{@render picked()}{/if}
    </Field>
  {/if}
  <Field label="Note" id="ts-note" hint={noteHint} error={noteErr}>
    {#snippet aside()}{note.length} / {TRANSFER_NOTE_MAX}{/snippet}
    <textarea id="ts-note" class="textarea" bind:value={note} rows="3" maxlength={TRANSFER_NOTE_MAX + 100} placeholder={notePlaceholder} {disabled} data-testid="send-note"></textarea>
  </Field>
</div>
