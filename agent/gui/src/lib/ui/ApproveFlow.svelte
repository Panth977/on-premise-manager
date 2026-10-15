<script lang="ts">
  /**
   * Approve / Roll back: POST …/start → 60 s countdown (Cancel · ⏩ Hold to fast-forward) → type ACCEPT →
   * POST approve|rollback → onstarted(run). The wait is enforced by the agent (§21 #95: effective seconds); IntentFlow.
   */
  import type { Snippet } from 'svelte';
  import type { UpdateRun } from '@opm/shared/types';
  import { api } from '../api';
  import IntentFlow from './IntentFlow.svelte';
  import { checkCommitMessage } from '@opm/shared/branch';

  interface Props {
    open: boolean;
    kind: 'update' | 'rollback';
    target: string;
    title: string;
    details?: Snippet;
    onstarted: (run: UpdateRun) => void;
  }
  let { open = $bindable(false), kind, target, title, details, onstarted }: Props = $props();
  /** §21 #104: a rollback writes a commit on this site's branch — its message is required */
  let message = $state('');
  const msgIssue = $derived(kind === 'rollback' ? (checkCommitMessage(message)[0]?.message ?? null) : null);
  const what = $derived(kind === 'rollback' ? 'the rollback' : 'the update');
  const intentApi = {
    get: (intentId: string) => api.getIntent({ intentId }),
    speed: (intentId: string, speed: 1 | 2 | 4) => api.intentSpeed({ intentId, body: { speed } }),
    cancel: (intentId: string) => api.cancelIntent({ intentId }),
  };
</script>

{#snippet commitMsg()}
  <div class="field">
    <label for="rb-message">Commit message <span class="req">*</span></label>
    <textarea id="rb-message" class="input" rows="2" bind:value={message} maxlength="2000" placeholder="Why roll back? (recorded on this site’s branch)" data-testid="rollback-message"></textarea>
    <div class="hint">The rollback is a new commit on this site’s branch (history is never rewritten).</div>
  </div>
{/snippet}

<IntentFlow
  bind:open
  {title}
  {details}
  {what}
  testid="approve-flow"
  cancelTestid="approve-cancel"
  confirmTestid="start-update"
  confirmLabel={kind === 'rollback' ? 'Start rollback' : 'Start update'}
  startingText="Checking the gate on every site…"
  waitText="Read what changes. {kind === 'rollback' ? 'The rollback' : 'The update'} cannot start before the wait ends; Cancel stops it (the next Approve starts the wait again)."
  {intentApi}
  confirmBlocked={msgIssue ? `Commit message: ${msgIssue}` : null}
  extra={kind === 'rollback' ? commitMsg : undefined}
  start={() => (kind === 'update' ? api.approveStart({ revId: target }) : api.rollbackStart({ snapshotId: target }))}
  confirm={async (body) => {
    const r = kind === 'update' ? await api.approve({ revId: target, body }) : await api.rollback({ snapshotId: target, body: { ...body, message: message.trim() } });
    onstarted(r.run);
  }}
>
  {#snippet accept()}
    Type <strong>ACCEPT</strong> to start {what} on all sites. A snapshot is taken first; any failure rolls back every site.
  {/snippet}
</IntentFlow>
