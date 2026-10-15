<script lang="ts">
  /**
   * §21 #108 opm-cloud password reset: /reset-password?token=… (admin-made links and reset e-mails on the VM point here).
   * GET /auth/reset/check → the e-mail, or "expired / invalid"; POST /auth/reset/confirm → /login?reset=1.
   * Firebase Hosting never links here (Firebase uses its own action URL); a token there simply shows "invalid".
   */
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { onMount } from 'svelte';
  import { session } from '$lib/session.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Field from '$lib/ui/Field.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Spinner from '$lib/ui/Spinner.svelte';
  import { CircleAlert, Eye, EyeOff } from '$lib/ui/icons';

  const MIN = 8;
  type View = { kind: 'checking' } | { kind: 'form'; email: string } | { kind: 'invalid'; message: string } | { kind: 'error'; message: string };
  let view = $state<View>({ kind: 'checking' });
  let password = $state('');
  let confirm = $state('');
  let showPw = $state(false);
  let touched = $state(false);
  let busy = $state(false);
  let error = $state<string | null>(null);

  const token = $derived(page.url.searchParams.get('token') ?? '');
  const pwErr = $derived(!password ? 'Required' : password.length < MIN ? `At least ${MIN} characters` : null);
  const confirmErr = $derived(!confirm ? 'Required' : confirm !== password ? 'The passwords do not match' : null);

  async function check() {
    view = { kind: 'checking' };
    const r = await session.checkPasswordReset(token);
    view = r.ok ? { kind: 'form', email: r.email } : r.reason === 'error' ? { kind: 'error', message: r.message } : { kind: 'invalid', message: r.message };
  }
  onMount(check);

  async function submit(e: Event) {
    e.preventDefault();
    touched = true;
    if (pwErr || confirmErr) return;
    busy = true;
    error = null;
    try {
      await session.confirmPasswordReset(token, password);
      goto('/login?reset=1', { replaceState: true });
    } catch (err) {
      error = (err as Error).message;
    } finally {
      busy = false;
    }
  }
</script>

<svelte:head><title>Set a new password · On-Prem Manager</title></svelte:head>

<main class="panel">
  <div class="card box" data-testid="reset-page">
    <div class="logo-row">
      <img src="/favicon.svg" alt="" width="24" height="24" />
      <span>On-Prem Manager</span>
    </div>
    {#if view.kind === 'checking'}
      <div class="center muted" aria-busy="true" data-testid="reset-checking"><Spinner size={18} /> Checking the link…</div>
    {:else if view.kind === 'invalid'}
      <div class="head"><h2>Link expired or invalid</h2></div>
      <div class="banner error" role="alert" data-testid="reset-invalid"><Icon icon={CircleAlert} size={16} class="icon" /><div>{view.message} Ask an admin for a new link, or use "Forgot password?" on the login page.</div></div>
      <Button variant="primary" block href="/login">Go to login</Button>
    {:else if view.kind === 'error'}
      <div class="head"><h2>Cannot check the link</h2></div>
      <div class="banner error" role="alert" data-testid="reset-error"><Icon icon={CircleAlert} size={16} class="icon" /><div>{view.message}</div></div>
      <Button variant="primary" block onclick={check}>Try again</Button>
    {:else}
      <form class="stack" onsubmit={submit} novalidate data-testid="reset-form">
        <div class="head">
          <h2>Set a new password</h2>
          <p class="muted">For <span class="strong" data-testid="reset-email">{view.email}</span></p>
        </div>
        {#if error}
          <div class="banner error" role="alert" data-testid="reset-submit-error"><Icon icon={CircleAlert} size={16} class="icon" /><div>{error}</div></div>
        {/if}
        <input type="email" autocomplete="username" value={view.email} hidden readonly />
        <Field label="New password" id="new-password" required hint={`At least ${MIN} characters`} error={touched ? pwErr : null}>
          <div class="pw">
            <input id="new-password" class="input" type={showPw ? 'text' : 'password'} autocomplete="new-password" bind:value={password} data-testid="reset-password" />
            <button type="button" class="btn ghost icon sm eye" aria-label={showPw ? 'Hide password' : 'Show password'} onclick={() => (showPw = !showPw)}><Icon icon={showPw ? EyeOff : Eye} size={14} /></button>
          </div>
        </Field>
        <Field label="Repeat the new password" id="confirm-password" required error={touched ? confirmErr : null}>
          <input id="confirm-password" class="input" type={showPw ? 'text' : 'password'} autocomplete="new-password" bind:value={confirm} data-testid="reset-confirm" />
        </Field>
        <Button type="submit" variant="primary" block loading={busy} data-testid="reset-submit">Set password</Button>
        <a class="small" href="/login">Back to login</a>
      </form>
    {/if}
  </div>
</main>

<style>
  .panel {
    min-height: 100vh;
    display: grid;
    place-items: center;
    padding: 24px 16px;
    background: var(--bg);
  }
  .box {
    width: 100%;
    max-width: 400px;
    padding: 28px;
    display: flex;
    flex-direction: column;
    gap: 16px;
    box-shadow: var(--shadow-md);
  }
  .stack {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .logo-row {
    display: flex;
    align-items: center;
    gap: 8px;
    font-weight: 600;
  }
  .head p {
    margin-top: 4px;
  }
  .center {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 12px 0;
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
</style>
