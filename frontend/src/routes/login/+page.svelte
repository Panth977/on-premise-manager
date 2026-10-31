<script lang="ts">
  import { goto, replaceState } from '$app/navigation';
  import { page } from '$app/state';
  import { onMount } from 'svelte';
  import { session } from '$lib/session.svelte';
  import { MOCK } from '$lib/env';
  import { RE } from '@opm/shared';
  import Button from '$lib/ui/Button.svelte';
  import Field from '$lib/ui/Field.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import { CircleAlert, Server, ShieldCheck, Boxes, Eye, EyeOff, KeyRound } from '$lib/ui/icons';

  let email = $state('');
  let password = $state('');
  let showPw = $state(false);
  let busy = $state(false);
  let oidcBusy = $state(false);
  let error = $state<string | null>(null);
  let touched = $state(false);
  let forgotOpen = $state(false);
  let forgotEmail = $state('');
  let forgotBusy = $state(false);
  let forgotSent = $state(false);

  const emailErr = $derived(!email ? 'Required' : !RE.email.test(email.trim()) ? 'Not an email address' : null);
  const pwErr = $derived(!password ? 'Required' : null);
  const next = $derived.by(() => {
    const n = page.url.searchParams.get('next');
    return n && n.startsWith('/app') ? n : '/app/deployments';
  });

  /** §21 #108 /reset-password success → "?reset=1" */
  const notice = $derived(page.url.searchParams.get('reset') === '1' ? 'Your password was changed. Log in with the new password.' : null);
  const oidc = $derived(session.authConfig.oidc);

  onMount(async () => {
    // §21 #108 opm-cloud OIDC comes back to /login#oidc=<one-time code>: drop the hash, then exchange the code
    const m = /^#oidc=([^&]+)/.exec(location.hash);
    await session.init();
    if (m) stripHash();
    if (session.status === 'signed-in') goto(next, { replaceState: true });
    else if (m) await finishOidc(decodeURIComponent(m[1]!));
    else if (session.loginError) error = session.loginError;
  });

  function stripHash() {
    const url = location.pathname + location.search;
    try {
      replaceState(url, {});
    } catch {
      history.replaceState(history.state, '', url); // router not started yet
    }
  }
  async function finishOidc(code: string) {
    oidcBusy = true;
    error = null;
    try {
      await session.loginWithOidcCode(code);
      goto(next, { replaceState: true });
    } catch (err) {
      error = (err as Error).message;
    } finally {
      oidcBusy = false;
    }
  }
  function startOidc() {
    oidcBusy = true;
    session.startOidc(next);
  }

  async function submit(e: Event) {
    e.preventDefault();
    touched = true;
    if (emailErr || pwErr) return;
    busy = true;
    error = null;
    try {
      await session.login(email, password);
      goto(next, { replaceState: true });
    } catch (err) {
      error = (err as Error).message;
    } finally {
      busy = false;
    }
  }
  function openForgot() {
    forgotEmail = email;
    forgotSent = false;
    forgotOpen = true;
  }
  async function sendForgot(e: Event) {
    e.preventDefault();
    if (!RE.email.test(forgotEmail.trim())) return;
    forgotBusy = true;
    await session.sendPasswordReset(forgotEmail);
    forgotBusy = false;
    forgotSent = true;
  }
</script>

<svelte:head><title>Log in · On-Prem Manager</title></svelte:head>

<div class="wrap">
  <aside class="brand">
    <div class="logo-row">
      <img src="/favicon.svg" alt="" width="32" height="32" />
      <span>On-Prem Manager</span>
    </div>
    <div class="pitch">
      <h1>Ship to machines you do not own.</h1>
      <p>Define a release in the cloud. The sites stage it on their own and change only after a person on site approves it.</p>
      <ul>
        <li><Icon icon={Boxes} size={16} /> Services, images and templates in one catalog</li>
        <li><Icon icon={Server} size={16} /> Every site runs one agent with its own edge</li>
        <li><Icon icon={ShieldCheck} size={16} /> Every action is signed, staged and logged</li>
      </ul>
    </div>
    <div class="foot faint small">OPM cloud console</div>
  </aside>

  <main class="panel">
    <form class="card form" onsubmit={submit} novalidate data-testid="login-form">
      <div class="head">
        <h2>Log in</h2>
        <p class="muted">Use the account an OPM admin created for you.</p>
      </div>
      {#if notice && !error}
        <div class="banner ok" role="status" data-testid="login-notice">{notice}</div>
      {/if}
      {#if error}
        <div class="banner error" role="alert" data-testid="login-error"><Icon icon={CircleAlert} size={16} class="icon" /><div>{error}</div></div>
      {/if}
      <Field label="Email" id="email" required error={touched ? emailErr : null}>
        <input id="email" class="input" type="email" autocomplete="username" bind:value={email} placeholder="you@company.com" data-testid="login-email" />
      </Field>
      <Field label="Password" id="password" required error={touched ? pwErr : null}>
        {#snippet aside()}{#if session.configReady && session.passwordResetOffered}<button type="button" class="link" onclick={openForgot} data-testid="forgot">Forgot password?</button>{/if}{/snippet}
        <div class="pw">
          <input id="password" class="input" type={showPw ? 'text' : 'password'} autocomplete="current-password" bind:value={password} data-testid="login-password" />
          <button type="button" class="btn ghost icon sm eye" aria-label={showPw ? 'Hide password' : 'Show password'} onclick={() => (showPw = !showPw)}><Icon icon={showPw ? EyeOff : Eye} size={14} /></button>
        </div>
      </Field>
      <Button type="submit" variant="primary" block loading={busy} data-testid="login-submit">Log in</Button>
      {#if oidc}
        <div class="or"><span>or</span></div>
        <Button block icon={KeyRound} loading={oidcBusy} onclick={startOidc} data-testid="login-oidc">Continue with {oidc.label}</Button>
      {/if}
      {#if MOCK}
        <div class="demo small">
          <div class="strong">Mock mode</div>
          <div class="muted">admin@example.com / admin123 · viewer@example.com / viewer123 · ops@example.com / ops12345</div>
        </div>
      {/if}
    </form>
  </main>
</div>

<Modal bind:open={forgotOpen} title="Reset your password" size="sm" busy={forgotBusy} testid="forgot-modal">
  {#if forgotSent}
    <div class="banner ok" data-testid="forgot-sent">If the account exists, an email was sent.</div>
  {:else}
    <form id="forgot-form" onsubmit={sendForgot} class="stack">
      <p class="muted small">We send a link to set a new password.</p>
      <Field label="Email" id="femail" required error={forgotEmail && !RE.email.test(forgotEmail.trim()) ? 'Not an email address' : null}>
        <input id="femail" class="input" type="email" bind:value={forgotEmail} data-autofocus />
      </Field>
    </form>
  {/if}
  {#snippet footer()}
    {#if forgotSent}
      <Button variant="primary" onclick={() => (forgotOpen = false)}>Close</Button>
    {:else}
      <Button onclick={() => (forgotOpen = false)} disabled={forgotBusy}>Cancel</Button>
      <Button type="submit" form="forgot-form" variant="primary" loading={forgotBusy} disabled={!RE.email.test(forgotEmail.trim())}>Send link</Button>
    {/if}
  {/snippet}
</Modal>

<style>
  .wrap {
    min-height: 100vh;
    display: grid;
    grid-template-columns: minmax(360px, 46%) 1fr;
    background: var(--bg);
  }
  .brand {
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
    font-weight: 600;
    font-size: 15px;
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
    color: #d4d4e4;
  }
  .pitch li {
    display: flex;
    gap: 10px;
    align-items: center;
  }
  .brand .foot {
    color: #8a8aa3;
  }
  .panel {
    display: grid;
    place-items: center;
    padding: 24px 16px;
  }
  .form {
    width: 100%;
    max-width: 400px;
    padding: 28px;
    display: flex;
    flex-direction: column;
    gap: 16px;
    box-shadow: var(--shadow-md);
  }
  .head p {
    margin-top: 4px;
  }
  .link {
    border: 0;
    background: transparent;
    color: var(--accent-text);
    cursor: pointer;
    padding: 0;
    font-size: var(--fs-xs);
  }
  .pw {
    position: relative;
  }
  .pw input {
    padding-right: 36px;
  }
  .eye {
    position: absolute;
    right: 3px;
    top: 3px;
  }
  .or {
    display: flex;
    align-items: center;
    gap: 10px;
    color: var(--text-3);
    font-size: var(--fs-xs);
  }
  .or::before,
  .or::after {
    content: '';
    flex: 1;
    height: 1px;
    background: var(--border);
  }
  .demo {
    border: 1px dashed var(--border-strong);
    border-radius: var(--r-md);
    padding: 8px 10px;
  }
  @media (max-width: 860px) {
    .wrap {
      grid-template-columns: 1fr;
    }
    .brand {
      padding: 24px;
      gap: 18px;
    }
    .pitch h1 {
      font-size: 22px;
    }
    .pitch ul,
    .brand .foot {
      display: none;
    }
  }
</style>
