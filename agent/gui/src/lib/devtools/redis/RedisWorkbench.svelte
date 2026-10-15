<script lang="ts">
  /**
   * Redis workbench (§21 #97): key browser (left) · Key value / Console / Info (right). Every write (value editors,
   * TTL, rename, delete, new key, console) is one executeRedis command; blocked commands are explained; destructive
   * ones ask for the typed connection name and are re-sent with confirm.
   */
  import type { DevToolConnection, DevToolsInfo, RedisKeyRow, RedisReply } from '@opm/shared/devtools';
  import { errMessage } from '$lib/api';
  import { toasts } from '$lib/state/toast.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import Select from '$lib/ui/Select.svelte';
  import Empty from '$lib/ui/Empty.svelte';
  import TypedConfirm from '../TypedConfirm.svelte';
  import { blockedCommand, confirmReasons, dev, newQueryId } from '../api';
  import { devStore } from '../state.svelte';
  import KeyBrowser from './KeyBrowser.svelte';
  import ValuePane from './ValuePane.svelte';
  import RedisConsole from './RedisConsole.svelte';
  import RedisInfo from './RedisInfo.svelte';
  import { quoteArg } from './redis';

  let { conn, site }: { conn: DevToolConnection; info: DevToolsInfo; site: string } = $props();
  const db = $derived(conn.auth.database ?? '0');
  type Pane = 'key' | 'console' | 'info';
  let pane = $state<Pane>('key');
  let selected = $state<string | null>(null);
  let scanV = $state(0);

  // ── one command, with the confirm flow
  let confirm = $state<{ open: boolean; reasons: string[]; what: string }>({ open: false, reasons: [], what: '' });
  let resolver: ((typed: string | null) => void) | null = null;
  function askConfirm(reasons: string[], what: string): Promise<string | null> {
    confirm = { open: true, reasons, what };
    return new Promise((r) => (resolver = r));
  }
  async function runCmd(argv: string[], typed?: string): Promise<{ reply: RedisReply | null; error: string | null; ms: number | null; cancelled?: boolean }> {
    try {
      const r = await dev.executeRedis({ siteSlug: site, connectionId: conn.id, body: { command: argv, queryId: newQueryId(), confirm: typed } });
      return { reply: r.reply, error: r.error, ms: r.durationMs };
    } catch (e) {
      const reasons = confirmReasons(e);
      if (reasons && typed === undefined) {
        const t = await askConfirm(reasons, argv.map(quoteArg).join(' '));
        if (t === null) return { reply: null, error: null, ms: null, cancelled: true };
        return runCmd(argv, t);
      }
      const blocked = blockedCommand(e);
      return { reply: null, error: blocked !== null ? `${errMessage(e)} (blocked: ${blocked})` : errMessage(e), ms: null };
    } finally {
      void devStore.refreshConns();
    }
  }
  async function exec(argv: string[]): Promise<boolean> {
    const r = await runCmd(argv);
    if (r.cancelled) return false;
    if (r.error) {
      toasts.push('bad', `${argv[0]} failed`, r.error);
      return false;
    }
    if (r.reply?.type === 'nil' && argv[0] === 'RENAMENX') return false;
    if (r.reply?.type === 'integer' && argv[0] === 'RENAMENX' && r.reply.value === 0) {
      toasts.push('bad', 'Not renamed', `${argv[2]} already exists.`);
      return false;
    }
    return true;
  }
  function select(k: RedisKeyRow) {
    selected = k.key;
    pane = 'key';
  }
  function changed(what: 'value' | 'deleted' | 'renamed', to?: string) {
    if (what === 'deleted') selected = null;
    if (what === 'renamed' && to) selected = to;
    if (what !== 'value') scanV++;
  }

  // ── new key
  const TYPES = ['string', 'hash', 'list', 'set', 'zset', 'stream'];
  let nk = $state({ open: false, key: '', type: 'string', a: '', b: '', ttl: '' });
  async function createKey() {
    const k = nk.key.trim();
    if (!k) return toasts.push('bad', 'Key name required');
    const ex = await runCmd(['EXISTS', k]);
    if (ex.reply?.type === 'integer' && ex.reply.value === 1) return toasts.push('bad', `${k} already exists`);
    const t = nk.type;
    const argv = t === 'string' ? ['SET', k, nk.a] : t === 'hash' ? ['HSET', k, nk.a, nk.b] : t === 'list' ? ['RPUSH', k, nk.a] : t === 'set' ? ['SADD', k, nk.a] : t === 'zset' ? ['ZADD', k, nk.b || '0', nk.a] : ['XADD', k, '*', nk.a, nk.b];
    if (!(await exec(argv))) return;
    if (Number(nk.ttl) > 0) await exec(['EXPIRE', k, String(Math.round(Number(nk.ttl)))]);
    toasts.ok(`Created ${k}`);
    nk.open = false;
    scanV++;
    selected = k;
    pane = 'key';
  }
</script>

<div class="rw" data-testid="redis-workbench">
  <aside class="left">
    <KeyBrowser {site} connectionId={conn.id} {selected} onselect={select} onnew={() => (nk = { open: true, key: '', type: 'string', a: '', b: '', ttl: '' })} version={scanV} />
  </aside>
  <section class="rcol">
    <div class="tabs" role="tablist">
      <button role="tab" aria-selected={pane === 'key'} class:active={pane === 'key'} onclick={() => (pane = 'key')} data-testid="redis-tab-key"><Icon name="keyRound" size={14} />Key</button>
      <button role="tab" aria-selected={pane === 'console'} class:active={pane === 'console'} onclick={() => (pane = 'console')} data-testid="redis-tab-console"><Icon name="terminal" size={14} />Console</button>
      <button role="tab" aria-selected={pane === 'info'} class:active={pane === 'info'} onclick={() => (pane = 'info')} data-testid="redis-tab-info"><Icon name="info" size={14} />Info</button>
      <span class="grow"></span>
      <span class="badge outline dbb" title="Redis database of this connection (SELECT is blocked: the db is part of the connection)">db {db}</span>
      {#if conn.readOnly}<span class="badge amber"><Icon name="lock" size={11} />read-only</span>{/if}
    </div>
    <div class="pane">
      {#if pane === 'key'}
        {#if selected}
          {#key selected}<ValuePane {site} connectionId={conn.id} keyName={selected} readOnly={conn.readOnly} {exec} onchanged={changed} />{/key}
        {:else}
          <div class="pad"><Empty icon="keyRound" title="Pick a key" text="Browse the keyspace on the left (tree split on “:”, or the flat list), or create a key." compact /></div>
        {/if}
      {:else if pane === 'console'}
        <div class="pad full"><RedisConsole {site} connectionId={conn.id} {db} run={runCmd} /></div>
      {:else}
        <RedisInfo {site} connectionId={conn.id} {db} />
      {/if}
    </div>
  </section>
</div>

<TypedConfirm
  bind:open={confirm.open}
  connectionName={conn.name}
  reasons={confirm.reasons}
  what={confirm.what}
  onconfirm={(t) => {
    resolver?.(t);
    resolver = null;
  }}
  oncancel={() => {
    resolver?.(null);
    resolver = null;
  }} />

<Modal bind:open={nk.open} title="New key" testid="redis-new-modal">
  <div class="stack">
    <div class="form-grid">
      <div class="field"><label for="nk-key">Key</label><input id="nk-key" class="input mono" bind:value={nk.key} placeholder="user:42" data-testid="redis-new-key" /></div>
      <div class="field"><span class="label">Type</span><Select value={nk.type} options={TYPES.map((t) => ({ value: t, label: t }))} onchange={(v) => (nk.type = v)} ariaLabel="Type" testid="redis-new-type" /></div>
    </div>
    <div class="field">
      <label for="nk-a">{nk.type === 'string' ? 'Value' : nk.type === 'hash' || nk.type === 'stream' ? 'Field' : 'First member'}</label>
      {#if nk.type === 'string'}<textarea id="nk-a" class="input mono" rows="5" bind:value={nk.a} data-testid="redis-new-a"></textarea>{:else}<input id="nk-a" class="input mono" bind:value={nk.a} data-testid="redis-new-a" />{/if}
    </div>
    {#if nk.type === 'hash' || nk.type === 'zset' || nk.type === 'stream'}
      <div class="field"><label for="nk-b">{nk.type === 'zset' ? 'Score' : 'Value'}</label><input id="nk-b" class="input mono" bind:value={nk.b} data-testid="redis-new-b" /></div>
    {/if}
    <div class="field"><label for="nk-ttl">TTL (seconds, optional)</label><input id="nk-ttl" class="input mono" type="number" min="1" bind:value={nk.ttl} placeholder="no expiry" /></div>
  </div>
  {#snippet footer()}
    <Btn variant="ghost" onclick={() => (nk.open = false)}>Cancel</Btn>
    <Btn variant="primary" onclick={createKey} disabled={conn.readOnly} data-testid="redis-new-ok">Create</Btn>
  {/snippet}
</Modal>

<style>
  .rw {
    display: grid;
    grid-template-columns: minmax(300px, 380px) minmax(0, 1fr);
    gap: 10px;
    height: calc(100dvh - 132px);
    min-height: 520px;
  }
  .left,
  .rcol {
    border: 1px solid var(--border);
    border-radius: var(--r-lg);
    background: var(--surface);
    min-height: 0;
    overflow: hidden;
    display: flex;
    flex-direction: column;
  }
  .rcol .tabs {
    padding: 0 8px;
    align-items: center;
    background: var(--surface-2);
  }
  .dbb {
    margin-right: 6px;
  }
  .pane {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
  }
  .pane > :global(*) {
    flex: 1;
    min-height: 0;
  }
  .pad {
    padding: 10px;
  }
  .full {
    display: flex;
    flex-direction: column;
  }
  @media (max-width: 900px) {
    .rw {
      grid-template-columns: minmax(0, 1fr);
      height: auto;
    }
    .left {
      height: 380px;
    }
    .rcol {
      min-height: 520px;
    }
  }
</style>
