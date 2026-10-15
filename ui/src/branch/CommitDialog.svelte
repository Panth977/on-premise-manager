<script lang="ts">
  /**
   * Commit dialog (§21 #104 D3): nothing reaches history without a message. A native modal <dialog> (browser top
   * layer), so it works the same in the agent GUI (nested in its dialogs) and the cloud console (above portaled drawers).
   * The message is NEVER prefilled silently — "Suggest" inserts an automatic summary on request.
   */
  import type { Snippet } from 'svelte';
  import type { ValidationIssue } from '@opm/shared/types';
  import { checkCommitMessage, commitSummary, COMMIT_MESSAGE_MAX, COMMIT_SUMMARY_MAX, suggestMessage, type MergeItem } from '@opm/shared/branch';
  import Btn from '../primitives/Btn.svelte';
  import Issues from '../primitives/Issues.svelte';
  import Icon from '../primitives/Icon.svelte';
  import { EFFECT_LABEL, groupItems } from './labels';
  interface Props {
    open?: boolean;
    title?: string;
    primaryLabel?: string;
    /** the changes this commit records (shown grouped, collapsible) */
    items?: readonly MergeItem[];
    /** validation of the result: errors block the commit */
    issues?: readonly ValidationIssue[];
    /** plain warnings (needs agent ≥ 0.14, compat proposals …) */
    warnings?: readonly string[];
    /** agent: what the apply engine does ("recreate api") */
    runtime?: readonly { containerSlug: string; action: string }[];
    /** text above the message (e.g. "Merge 4 changes from Single@7 into Test 02") */
    intro?: string;
    suggestPrefix?: string;
    author?: string;
    busy?: boolean;
    /** extra reason the commit is not possible now */
    blocked?: string | null;
    onsubmit: (message: string) => Promise<void> | void;
    onclose?: () => void;
    danger?: boolean;
    /** §21 #105 an info line (e.g. "Rebased onto commit 5 — review and commit again") */
    info?: string | null;
    /** §21 #105 extra content above the buttons (e.g. agent: "Commit server changes first…") */
    extra?: Snippet;
    /** §21 #107 progress of the commit transaction ("Uploading 2 of 5 files · 1.2 MB of 3 MB", "Committing…") */
    progress?: string | null;
    /** §21 #107 pending bytes this commit uploads first ("3 files · 1.2 MB to upload with this commit") */
    uploadNote?: string | null;
    testid?: string;
  }
  let { open = $bindable(false), title = 'Commit', primaryLabel = 'Commit', items = [], issues = [], warnings = [], runtime = [], intro, suggestPrefix = '', author, busy = false, blocked = null, onsubmit, onclose, danger = false, info = null, extra, progress = null, uploadNote = null, testid = 'commit-dialog' }: Props = $props();
  let dlg = $state<HTMLDialogElement>();
  let message = $state('');
  let touched = $state(false);
  let error = $state<string | null>(null);
  let showChanges = $state(false);
  const msgIssues = $derived(checkCommitMessage(message));
  const first = $derived(commitSummary(message));
  const errs = $derived(issues.filter((i) => i.severity === 'error').length);
  const groups = $derived(groupItems(items));
  $effect(() => {
    if (!dlg) return;
    if (open && !dlg.open) {
      message = '';
      touched = false;
      error = null;
      showChanges = items.length <= 12;
      dlg.showModal();
    } else if (!open && dlg.open) dlg.close();
  });
  function close() {
    if (busy) return;
    open = false;
    onclose?.();
  }
  async function submit(e: Event) {
    e.preventDefault();
    touched = true;
    if (msgIssues.length || errs || blocked || busy) return;
    error = null;
    try {
      await onsubmit(message.trim());
    } catch (x) {
      error = x instanceof Error ? x.message : String(x);
    }
  }
</script>

<dialog bind:this={dlg} class="cd" data-testid={testid} aria-labelledby="{testid}-title" oncancel={(e) => { e.preventDefault(); close(); }}>
  <form method="dialog" onsubmit={submit} class="stack">
    <header class="row between">
      <h3 id="{testid}-title">{title}</h3>
      <Btn size="sm" variant="ghost" icon="x" aria-label="Close" onclick={close} disabled={busy} data-testid="{testid}-close" />
    </header>
    {#if intro}<p class="muted small m0">{intro}</p>{/if}
    <div class="field">
      <div class="row between">
        <label for="{testid}-msg" class="label">Commit message <span class="req">*</span></label>
        <Btn size="xs" variant="ghost" icon="edit" disabled={!items.length} onclick={() => (message = suggestMessage(items, suggestPrefix) + (message.trim() ? '\n\n' + message.trim() : ''))} data-testid="commit-suggest">Suggest</Btn>
      </div>
      <!-- svelte-ignore a11y_autofocus -->
      <textarea id="{testid}-msg" class="textarea" rows="4" maxlength={COMMIT_MESSAGE_MAX + 200} placeholder="What changed and why (first line = summary)" bind:value={message} oninput={() => (touched = true)} aria-invalid={touched && msgIssues.length > 0} data-testid="commit-message" autofocus></textarea>
      <div class="row between tiny muted">
        <span class:bad-text={first.length > COMMIT_SUMMARY_MAX}>summary {first.length} / {COMMIT_SUMMARY_MAX}</span>
        <span class:bad-text={message.trim().length > COMMIT_MESSAGE_MAX}>{message.trim().length} / {COMMIT_MESSAGE_MAX}</span>
      </div>
      {#if touched && msgIssues.length}<div class="field-err" data-testid="commit-message-error">{msgIssues[0].message}</div>{/if}
    </div>
    {#if author}<p class="tiny muted m0"><Icon name="user" size={12} /> Author: <span class="mono">{author}</span></p>{/if}
    {#if items.length}
      <div class="changes">
        <button type="button" class="linkish small" onclick={() => (showChanges = !showChanges)} data-testid="commit-changes-toggle" aria-expanded={showChanges}>
          <Icon name={showChanges ? 'chevronDown' : 'chevronRight'} size={13} />{items.length} change{items.length === 1 ? '' : 's'}
        </button>
        {#if showChanges}
          <div class="list" data-testid="commit-changes">
            {#each groups as g (g.id)}
              <div class="g"><div class="tiny strong">{g.title}</div>
                <ul>{#each g.items as i (i.key)}<li class="small"><span class="badge {i.effect === 'remove' ? 'red' : i.effect === 'add' ? 'green' : 'blue'}">{EFFECT_LABEL[i.effect]}</span> {i.label}</li>{/each}</ul>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    {/if}
    {#if runtime.length}
      <div class="banner info small" data-testid="commit-runtime"><Icon name="container" /><div class="grow">On this site: {runtime.map((r) => `${r.action} ${r.containerSlug}`).join(' · ')}</div></div>
    {/if}
    {#each warnings as w, k (k)}<div class="banner warn small" data-testid="commit-warning" data-index={k} data-warning={w.includes('0.14') ? 'needs-agent-branches' : w.includes('needs agent') || w.includes('agent ≥') ? 'needs-agent' : 'other'}><Icon name="alert" /><div class="grow">{w}</div></div>{/each}
    {#if issues.length}<Issues {issues} title="Result" testid="commit-issues" />{/if}
    {#if info}<div class="banner info small" data-testid="commit-info"><Icon name="info" /><div class="grow">{info}</div></div>{/if}
    {#if blocked}<div class="banner bad small" data-testid="commit-blocked"><Icon name="alert" /><div class="grow">{blocked}</div></div>{/if}
    {#if extra}{@render extra()}{/if}
    {#if uploadNote && !progress}<div class="banner info small" data-testid="commit-upload-note"><Icon name="upload" /><div class="grow">{uploadNote}</div></div>{/if}
    {#if progress}<div class="banner info small" data-testid="commit-progress" aria-live="polite"><Icon name="upload" /><div class="grow">{progress}</div></div>{/if}
    {#if error}<div class="banner bad small" data-testid="commit-error"><Icon name="alert" /><div class="grow">{error}</div></div>{/if}
    <footer class="row end">
      <Btn variant="ghost" onclick={close} disabled={busy} data-testid="commit-cancel">Cancel</Btn>
      <Btn type="submit" variant={danger ? 'danger' : 'primary'} {busy} disabled={msgIssues.length > 0 || errs > 0 || !!blocked} data-testid="commit-submit">{primaryLabel}</Btn>
    </footer>
  </form>
</dialog>

<style>
  .cd {
    width: min(560px, calc(100vw - 24px));
    max-height: calc(100vh - 32px);
    border: 1px solid var(--border);
    border-radius: var(--r-xl);
    padding: 16px;
    background: var(--surface);
    color: var(--text);
    box-shadow: var(--shadow-lg, 0 20px 50px rgba(0, 0, 0, 0.25));
  }
  .cd::backdrop {
    background: var(--overlay);
  }
  h3 {
    margin: 0;
    font-size: var(--fs-lg);
  }
  .m0 {
    margin: 0;
  }
  .changes .list {
    max-height: 220px;
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    padding: 6px 10px;
    margin-top: 6px;
  }
  .changes ul {
    margin: 2px 0 6px;
    padding-left: 0;
    list-style: none;
  }
  .linkish {
    background: none;
    border: 0;
    padding: 0;
    color: var(--accent-text);
    cursor: pointer;
    display: inline-flex;
    gap: 4px;
    align-items: center;
  }
  footer {
    gap: 8px;
  }
</style>
