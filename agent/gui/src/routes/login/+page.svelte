<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { goto } from '$app/navigation';
  import { base } from '$app/paths';
  import { page } from '$app/state';
  import { api, errCode, errMessage, isApiError } from '$lib/api';
  import { session } from '$lib/state/session.svelte';
  import { mmss } from '$lib/format';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Brand from '$lib/ui/Brand.svelte';
  import { deploymentLabel } from '$lib/cloud';
  import type { AgentInfoResponse } from '@opm/shared/types';

  const FAILS_KEY = 'opm.loginFails';
  let info = $state<AgentInfoResponse | null>(null);
  let infoError = $state<string | null>(null);
  let key = $state('');
  let busy = $state(false);
  let error = $state<string | null>(null);
  let lockedUntil = $state(0);
  let input: HTMLInputElement | undefined = $state();

  const normalized = $derived(key.toUpperCase().replace(/[^A-Z2-7]/g, '').slice(0, 24));
  const lockLeft = $derived(Math.max(0, (lockedUntil - session.tick) / 1000));
  const next = $derived.by(() => {
    const n = page.url.searchParams.get('next') ?? '';
    return n.startsWith(`${base}/app`) ? n : `${base}/app`;
  });

  function group(v: string): string {
    const n = v.toUpperCase().replace(/[^A-Z2-7]/g, '').slice(0, 24);
    return n.replace(/(.{4})(?=.)/g, '$1-');
  }
  function onInput(e: Event) {
    const el = e.currentTarget as HTMLInputElement;
    key = group(el.value);
    el.value = key;
  }
  function localFails(): number[] {
    try {
      const v = JSON.parse(sessionStorage.getItem(FAILS_KEY) ?? '[]') as number[];
      return v.filter((t) => Date.now() - t < 300_000);
    } catch {
      return [];
    }
  }
  function addLocalFail(): number {
    const v = [...localFails(), Date.now()];
    try {
      sessionStorage.setItem(FAILS_KEY, JSON.stringify(v));
    } catch {
      /* blocked */
    }
    return v.length;
  }

  let alive = true;
  onDestroy(() => (alive = false));
  onMount(async () => {
    try {
      info = await api.info({});
      session.info = info;
      session.setServerTime(info.serverTime);
    } catch (e) {
      infoError = errMessage(e);
    }
    // already logged in (cookie still valid) → straight on
    try {
      const s = await api.session({});
      if (!alive || busy) return;
      session.session = s;
      session.endedReason = null;
      goto(next, { replaceState: true });
    } catch {
      /* not logged in */
    }
    if (alive) input?.focus();
  });

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (normalized.length !== 24 || busy || lockLeft > 0) return;
    busy = true;
    error = null;
    try {
      const r = await api.login({ body: { accessKey: normalized } });
      session.session = { sessionId: r.sessionId, expiresAt: r.expiresAt, keyVersion: r.keyVersion };
      session.endedReason = null;
      session.actor = null;
      try {
        sessionStorage.removeItem(FAILS_KEY);
      } catch {
        /* blocked */
      }
      await goto(next, { replaceState: true });
    } catch (err) {
      const code = errCode(err);
      if (code === 'invalid-key') {
        const n = isApiError(err) && typeof err.details.failures === 'number' ? err.details.failures : addLocalFail();
        error = `Wrong access key (${Math.min(n, 5)} of 5).`;
        input?.select();
      } else if (code === 'rate-limited') {
        const sec = isApiError(err) && typeof err.details.retryAfterSec === 'number' ? err.details.retryAfterSec : Math.max(1, Math.ceil(((localFails()[0] ?? Date.now()) + 300_000 - Date.now()) / 1000));
        lockedUntil = Date.now() + sec * 1000;
        error = null;
      } else if (code === 'unavailable') {
        error = 'This site has no access key yet. It gets it from the cloud or from another site.';
      } else error = errMessage(err);
    } finally {
      busy = false;
    }
  }
</script>

<svelte:head><title>Log in · OPM agent</title></svelte:head>

<div class="wrap">
  <aside class="side">
    <div class="logo-row"><Brand size={32} /><span>On-Prem Manager</span><span class="agent">agent</span></div>
    <div class="pitch">
      <h1>This machine is one site of a deployment.</h1>
      <p>The agent runs the containers and the edge of this site, stages every release from the cloud and applies it only after a person here approves it.</p>
      <ul>
        <li><Icon name="layers" size={16} /> Releases stage on their own; you approve them</li>
        <li><Icon name="archive" size={16} /> A snapshot before every update, restore any time</li>
        <li><Icon name="shield" size={16} /> Every action is signed and logged with your e-mail</li>
      </ul>
    </div>
    <div class="foot small">OPM agent{#if info}&nbsp;{info.agentVersion}{/if} · works offline on the site network</div>
  </aside>

  <main class="panel">
  <div class="form card">
    <div class="head">
      <h2>Log in to this site</h2>
      <p class="muted">Use the access key of the deployment{#if info && deploymentLabel(info)}&nbsp;<strong>{deploymentLabel(info)}</strong>{/if}.</p>
    </div>

    <dl class="kv ident">
      <dt>Deployment</dt>
      <dd>{#if info}<code data-testid="login-deployment">{info.deploymentId}</code>{:else if infoError}<span class="bad-text">{infoError}</span>{:else}<span class="skel" style="width:160px"></span>{/if}</dd>
      <dt>This site</dt>
      <dd>{#if info}<strong>{info.siteSlug}</strong> <span class="muted small">· agent {info.agentVersion} · key v{info.keyVersion}</span>{:else}<span class="skel" style="width:90px"></span>{/if}</dd>
    </dl>

    {#if session.endedReason}
      <div class="banner warn small" role="status"><Icon name="clock" /><div class="grow">{session.endedReason}</div></div>
    {/if}
    {#if info && !info.hasAccessKey}
      <div class="banner warn small"><Icon name="alert" /><div class="grow">This site has no access key yet. It gets it from the cloud or from another site.</div></div>
    {/if}

    <form onsubmit={submit} novalidate>
      <div class="field">
        <label for="key">Access key <span class="req" aria-hidden="true">*</span></label>
        <input
          bind:this={input}
          id="key"
          class="input mono keyin"
          value={key}
          oninput={onInput}
          placeholder="XXXX-XXXX-XXXX-XXXX-XXXX-XXXX"
          autocomplete="off"
          autocapitalize="characters"
          spellcheck="false"
          aria-invalid={error ? 'true' : undefined}
          aria-describedby="key-hint"
          required
          data-testid="access-key"
        />
        <div id="key-hint" class="row between hint">
          <span>24 characters (A–Z, 2–7). Dashes, spaces and case do not matter.</span>
          <span class:ok-text={normalized.length === 24}>{normalized.length}/24</span>
        </div>
      </div>
      {#if lockLeft > 0}
        <div class="banner bad small" role="alert" data-testid="login-error"><Icon name="lock" /><div class="grow">Too many attempts. Try again in {mmss(lockLeft)}.</div></div>
      {:else if error}
        <div class="banner bad small" role="alert" data-testid="login-error"><Icon name="alert" /><div class="grow">{error}</div></div>
      {/if}
      <Btn type="submit" variant="primary" {busy} disabled={normalized.length !== 24 || lockLeft > 0} class="full">Log in</Btn>
    </form>
    <p class="muted small foot" data-testid="key-help">After login the agent asks who is working right now. The access key is shared by the whole deployment. Find it on the cloud Setup page of this deployment: <strong>View</strong> shows it any time (logged), and Regenerate makes a new one.</p>
  </div>
  </main>
</div>

<style>
  /* same split layout as the cloud console login (frontend/src/routes/login) */
  .wrap {
    min-height: 100dvh;
    display: grid;
    grid-template-columns: minmax(360px, 46%) 1fr;
    background: var(--bg);
  }
  .side {
    background:
      radial-gradient(900px 500px at -10% -10%, rgba(99, 102, 241, 0.35), transparent 60%),
      radial-gradient(700px 500px at 110% 120%, rgba(14, 165, 233, 0.25), transparent 60%),
      #0f1020;
    color: #e8e8f2;
    padding: 40px 48px;
    display: flex;
    flex-direction: column;
    justify-content: space-between;
  }
  .logo-row {
    display: flex;
    align-items: center;
    gap: 10px;
    font-weight: var(--fw-semibold);
    font-size: 15px;
  }
  .agent {
    font-size: var(--fs-xs);
    font-weight: var(--fw-medium);
    padding: 2px 7px;
    border-radius: var(--r-full);
    background: rgba(165, 161, 255, 0.18);
    color: #c7c4ff;
  }
  .pitch h1 {
    font-size: 30px;
    line-height: 1.15;
    letter-spacing: -0.02em;
    max-width: 440px;
  }
  .pitch p {
    color: #b8b8cc;
    margin-top: 14px;
    max-width: 440px;
    font-size: 15px;
  }
  .pitch ul {
    list-style: none;
    padding: 0;
    margin: 28px 0 0;
    display: flex;
    flex-direction: column;
    gap: 12px;
    color: #cfcfe0;
  }
  .pitch li {
    display: flex;
    gap: 10px;
    align-items: center;
  }
  .side .foot {
    color: #8a8aa3;
    margin: 0;
  }
  .panel {
    display: grid;
    place-items: center;
    padding: 24px 16px;
  }
  .form {
    width: 100%;
    max-width: 420px;
    padding: 28px;
    display: flex;
    flex-direction: column;
    gap: 16px;
    box-shadow: var(--shadow-md);
  }
  .head h2 {
    font-size: var(--fs-xl);
  }
  .head p {
    margin: 4px 0 0;
  }
  .ident {
    padding: 10px 12px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    font-size: var(--fs-sm);
  }
  form {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .keyin {
    height: 40px;
    font-size: 14px;
    letter-spacing: 0.06em;
    text-align: center;
  }
  :global(.btn.full) {
    width: 100%;
  }
  .form > .foot {
    margin: 0;
  }
  @media (max-width: 860px) {
    .wrap {
      grid-template-columns: 1fr;
      grid-template-rows: auto 1fr;
    }
    .side {
      padding: 24px;
      gap: 18px;
    }
    .pitch h1 {
      font-size: 22px;
    }
    .pitch p,
    .pitch ul,
    .side .foot {
      display: none;
    }
  }
</style>
