<script lang="ts">
  /** "Who is working right now?" — blocking on every page load (SPEC §4.6 / §18). Change person: same popup + Cancel. */
  import { untrack } from 'svelte';
  import { api, errMessage } from '../api';
  import { session, recentLocalActors, rememberActor, forgetActor } from '../state/session.svelte';
  import Modal from './Modal.svelte';
  import Btn from './Btn.svelte';
  import Icon from './Icon.svelte';
  import { relTime } from '../format';
  import type { ActorRow } from '@opm/shared/types';

  let { open, cancellable = false, ondone }: { open: boolean; cancellable?: boolean; ondone: () => void } = $props();
  const EMAIL = /^[^@\s]+@[^@\s]+\.[^@\s]+$/;
  let email = $state('');
  let busy = $state(false);
  let error = $state<string | null>(null);
  let remote = $state<ActorRow[]>([]);
  let local = $state<string[]>([]);
  let input: HTMLInputElement | undefined = $state();

  $effect(() => {
    if (open) untrack(reset);
  });
  function reset() {
    local = recentLocalActors();
    email = '';
    error = null;
    api
      .recentActors({ query: { limit: 10 } })
      .then((r) => (remote = r.actors))
      .catch(() => (remote = []));
    setTimeout(() => input?.focus(), 30);
  }

  const suggestions = $derived.by(() => {
    const out: { email: string; where: 'this browser' | 'deployment'; last?: string }[] = local.map((e) => ({ email: e, where: 'this browser' as const }));
    for (const a of remote) if (!out.some((x) => x.email === a.email)) out.push({ email: a.email, where: 'deployment', last: a.lastUsedAt });
    const q = email.trim().toLowerCase();
    return (q ? out.filter((s) => s.email.includes(q)) : out).slice(0, 8);
  });
  const valid = $derived(EMAIL.test(email.trim()) && email.trim().length <= 254);

  async function submit(e?: SubmitEvent) {
    e?.preventDefault();
    if (!valid || busy) {
      if (!valid) error = 'Enter a valid email address (name@company.com).';
      return;
    }
    busy = true;
    error = null;
    try {
      const r = await api.declareActor({ body: { email: email.trim().toLowerCase() } });
      session.actor = { email: r.email, token: r.actorToken, expiresAt: r.expiresAt };
      session.askActor = false;
      rememberActor(r.email);
      ondone();
    } catch (err) {
      error = errMessage(err);
    } finally {
      busy = false;
    }
  }
</script>

<Modal {open} title="Who is working right now?" size="sm" closable={false} testid="actor-popup">
  <form onsubmit={submit} class="stack" novalidate>
    <p class="muted small" style="margin:0">This does not log you in. It names the person who works now; the agent writes this email into the activity log.</p>
    <div class="field">
      <label for="actor-email">Your email <span class="req" aria-hidden="true">*</span></label>
      <input bind:this={input} id="actor-email" class="input" type="email" bind:value={email} placeholder="name@company.com" autocomplete="email" aria-invalid={error ? 'true' : undefined} required data-testid="actor-email" />
      {#if error}<div class="field-err" role="alert">{error}</div>{/if}
    </div>
    {#if suggestions.length}
      <div>
        <div class="section-title" style="margin-bottom:4px">Recent</div>
        <ul class="sugg" role="listbox" aria-label="Recent people">
          {#each suggestions as s (s.email)}
            <li class="row">
              <button type="button" class="pick grow" role="option" aria-selected={email === s.email} onclick={() => ((email = s.email), input?.focus())} data-testid="actor-suggestion">
                <Icon name="user" size={14} />
                <span class="grow">{s.email}</span>
                <span class="faint tiny">{s.where === 'this browser' ? 'this browser' : relTime(s.last)}</span>
              </button>
              {#if s.where === 'this browser'}
                <button type="button" class="btn ghost xs icon" aria-label="Forget {s.email} in this browser" title="Forget in this browser" onclick={() => (forgetActor(s.email), (local = recentLocalActors()))}><Icon name="x" size={12} /></button>
              {/if}
            </li>
          {/each}
        </ul>
      </div>
    {/if}
    <div class="row end">
      {#if cancellable && session.actor}
        <Btn onclick={() => ((session.askActor = false), ondone())}>Cancel</Btn>
      {/if}
      <Btn type="submit" variant="primary" {busy} data-testid="actor-continue">Continue</Btn>
    </div>
  </form>
</Modal>

<style>
  .sugg {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
    max-height: 230px;
    overflow: auto;
  }
  .sugg li {
    gap: 2px;
    flex-wrap: nowrap;
  }
  .pick {
    display: flex;
    gap: 8px;
    align-items: center;
    font: inherit;
    font-size: 13px;
    text-align: left;
    background: none;
    border: 0;
    border-radius: 7px;
    padding: 7px 8px;
    cursor: pointer;
    color: var(--text);
    min-width: 0;
  }
  .pick span {
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .pick:hover,
  .pick[aria-selected='true'] {
    background: var(--gray-soft);
  }
</style>
