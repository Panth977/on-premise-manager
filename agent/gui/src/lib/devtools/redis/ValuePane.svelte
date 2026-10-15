<script lang="ts">
  /**
   * Value of one Redis key by type (§21 #97): string (JSON / text editor + Save = SET … KEEPTTL), hash / list / set /
   * zset / stream tables with paging and add / edit / delete members; TTL edit / persist, rename, delete. Every write
   * is a command sent through executeRedis (logged, classified by the agent).
   */
  import type { RedisHashValue, RedisKeyValueResponse, RedisListValue, RedisStreamValue, RedisZsetValue } from '@opm/shared/devtools';
  import { errMessage } from '$lib/api';
  import { toasts } from '$lib/state/toast.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import Confirm from '$lib/ui/Confirm.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import Editor from '../cm/Editor.svelte';
  import { dev } from '../api';
  import { copyText, isTrunc } from '../grid';
  import { TYPE_TONE, isJsonText, ttlText } from './redis';

  interface Props {
    site: string;
    connectionId: string;
    keyName: string;
    readOnly: boolean;
    /** run one command (confirm flow included); null = failed / cancelled */
    exec: (argv: string[]) => Promise<boolean>;
    onchanged: (what: 'value' | 'deleted' | 'renamed', newKey?: string) => void;
  }
  let { site, connectionId, keyName, readOnly, exec, onchanged }: Props = $props();
  let v = $state<RedisKeyValueResponse | null>(null);
  let err = $state<string | null>(null);
  type Row = RedisHashValue[number] | RedisZsetValue[number] | RedisStreamValue[number] | RedisListValue[number];
  let rows = $state<Row[]>([]);
  let loadingMore = $state(false);
  async function load() {
    err = null;
    try {
      const r = await dev.redisKey({ siteSlug: site, connectionId, query: { key: keyName, count: 100 } });
      v = r;
      rows = Array.isArray(r.value) ? (r.value as Row[]) : [];
      if (r.type === 'string') {
        text = isTrunc(r.value) ? r.value.preview : typeof r.value === 'string' ? r.value : JSON.stringify(r.value);
        orig = text;
        fmt = isJsonText(text) ? 'json' : 'text';
        if (fmt === 'json') text = orig = prettyMaybe(text);
      }
    } catch (e) {
      err = errMessage(e);
      v = null;
    }
  }
  $effect(() => {
    void keyName;
    load();
  });
  async function more() {
    if (!v?.next) return;
    loadingMore = true;
    try {
      const r = await dev.redisKey({ siteSlug: site, connectionId, query: { key: keyName, cursor: String(v.next), count: 100 } });
      rows = [...rows, ...(Array.isArray(r.value) ? (r.value as Row[]) : [])];
      v = { ...v, next: r.next };
    } catch (e) {
      toasts.err(e, 'Could not load more');
    } finally {
      loadingMore = false;
    }
  }

  // string
  type Fmt = 'json' | 'text';
  let text = $state('');
  let orig = $state('');
  let fmt = $state<Fmt>('text');
  function prettyMaybe(s: string) {
    try {
      return JSON.stringify(JSON.parse(s), null, 2);
    } catch {
      return s;
    }
  }
  let saving = $state(false);
  async function saveString() {
    let out = text;
    if (fmt === 'json') {
      try {
        out = JSON.stringify(JSON.parse(text));
      } catch (e) {
        return toasts.push('bad', 'Not valid JSON', e instanceof Error ? e.message : String(e));
      }
    }
    saving = true;
    if (await exec(['SET', keyName, out, 'KEEPTTL'])) {
      toasts.ok(`Saved ${keyName}`);
      await load();
      onchanged('value');
    }
    saving = false;
  }

  // ttl / rename / delete
  let ttlOpen = $state(false);
  let ttlVal = $state('');
  let renOpen = $state(false);
  let renVal = $state('');
  let delOpen = $state(false);
  async function setTtl(persist: boolean) {
    const n = Number(ttlVal);
    if (!persist && !(n > 0)) return toasts.push('bad', 'TTL in seconds (> 0)');
    if (await exec(persist ? ['PERSIST', keyName] : ['EXPIRE', keyName, String(Math.round(n))])) {
      ttlOpen = false;
      toasts.ok(persist ? 'Expiry removed' : `Expires in ${ttlText(Math.round(n))}`);
      await load();
      onchanged('value');
    }
  }
  async function rename() {
    const to = renVal.trim();
    if (!to || to === keyName) return;
    if (await exec(['RENAMENX', keyName, to])) {
      renOpen = false;
      toasts.ok(`Renamed to ${to}`);
      onchanged('renamed', to);
    }
  }
  async function del() {
    if (await exec(['DEL', keyName])) {
      toasts.ok(`Deleted ${keyName}`);
      onchanged('deleted');
    }
  }

  // members
  let mem = $state<{ open: boolean; mode: 'add' | 'edit'; a: string; b: string; old: string | null; idx: number | null }>({ open: false, mode: 'add', a: '', b: '', old: null, idx: null });
  const type = $derived(v?.type ?? '');
  const memLabels = $derived(type === 'hash' ? ['Field', 'Value'] : type === 'zset' ? ['Member', 'Score'] : type === 'stream' ? ['Field', 'Value'] : type === 'list' ? ['Value', ''] : ['Member', '']);
  function openAdd() {
    mem = { open: true, mode: 'add', a: '', b: type === 'zset' ? '0' : '', old: null, idx: null };
  }
  function openEdit(a: string, b: string, idx: number | null = null) {
    mem = { open: true, mode: 'edit', a, b, old: a, idx };
  }
  async function saveMember() {
    const { a, b, mode, old, idx } = mem;
    let argv: string[] | null = null;
    if (type === 'hash') argv = ['HSET', keyName, a, b];
    else if (type === 'zset') argv = ['ZADD', keyName, b, a];
    else if (type === 'set') argv = ['SADD', keyName, a];
    else if (type === 'list') argv = mode === 'edit' && idx !== null ? ['LSET', keyName, String(idx), a] : ['RPUSH', keyName, a];
    else if (type === 'stream') argv = ['XADD', keyName, '*', a, b];
    if (!argv) return;
    if (mode === 'edit' && type === 'set' && old !== null && old !== a && !(await exec(['SREM', keyName, old]))) return;
    if (mode === 'edit' && type === 'zset' && old !== null && old !== a && !(await exec(['ZREM', keyName, old]))) return;
    if (mode === 'edit' && type === 'hash' && old !== null && old !== a && !(await exec(['HDEL', keyName, old]))) return;
    if (await exec(argv)) {
      mem.open = false;
      await load();
      onchanged('value');
    }
  }
  async function removeMember(a: string) {
    const argv = type === 'hash' ? ['HDEL', keyName, a] : type === 'zset' ? ['ZREM', keyName, a] : type === 'set' ? ['SREM', keyName, a] : type === 'list' ? ['LREM', keyName, '1', a] : type === 'stream' ? ['XDEL', keyName, a] : null;
    if (argv && (await exec(argv))) {
      await load();
      onchanged('value');
    }
  }
  const s = (x: unknown) => (isTrunc(x) ? `${x.preview}… (${x.sizeBytes} bytes, cut — too large to show inline)` : typeof x === 'string' ? x : JSON.stringify(x));
  let viewVal = $state<string | null>(null);
</script>

<div class="vp" data-testid="redis-value">
  {#if err}
    <div class="banner bad"><Icon name="alert" /><div class="grow">{err}</div><button class="btn sm" onclick={load}>Retry</button></div>
  {:else if !v}
    <span class="skel" style="width:100%;height:160px"></span>
  {:else}
    <div class="vh">
      <span class="badge {TYPE_TONE[v.type] ?? 'accent'}">{v.type}</span>
      <span class="key mono" title={v.key} data-testid="redis-key-name">{v.key}</span>
      <button class="btn ghost icon xs" onclick={async () => (await copyText(v!.key)) && toasts.ok('Key copied')} aria-label="Copy the key name" title="Copy the key name"><Icon name="copy" size={12} /></button>
      <span class="grow"></span>
      <button class="ttlb" class:exp={v.ttlSec >= 0} onclick={() => ((ttlVal = v!.ttlSec > 0 ? String(v!.ttlSec) : '3600'), (ttlOpen = true))} disabled={readOnly} data-testid="redis-ttl"><Icon name="timer" size={12} />{v.ttlSec === -1 ? 'no expiry' : `TTL ${ttlText(v.ttlSec)}`}</button>
      {#if v.length !== null}<span class="faint xs">{v.type === 'string' ? `${v.length.toLocaleString()} bytes` : `${v.length.toLocaleString()} ${v.type === 'hash' ? 'fields' : v.type === 'stream' ? 'entries' : 'items'}`}</span>{/if}
      {#if v.encoding}<span class="faint xs mono" title="OBJECT ENCODING">{v.encoding}</span>{/if}
      <Btn size="xs" variant="ghost" icon="refresh" onclick={load} aria-label="Reload" title="Reload"></Btn>
      {#if !readOnly}
        <Btn size="xs" variant="ghost" icon="edit" onclick={() => ((renVal = v!.key), (renOpen = true))} data-testid="redis-rename">Rename</Btn>
        <Btn size="xs" variant="ghost" icon="trash" onclick={() => (delOpen = true)} data-testid="redis-delete">Delete</Btn>
      {/if}
    </div>

    {#if v.type === 'string'}
      <div class="strbar">
        <SegmentedControl options={[{ value: 'text' as Fmt, label: 'Text' }, { value: 'json' as Fmt, label: 'JSON', disabled: !isJsonText(text) && fmt !== 'json' }]} value={fmt} onchange={(f) => ((fmt = f), f === 'json' && (text = prettyMaybe(text)))} label="Format" size="sm" testid="redis-fmt" />
        {#if isTrunc(v.value)}<span class="badge amber">cut: {v.value.sizeBytes.toLocaleString()} bytes — saving would overwrite with the preview</span>{/if}
        <span class="grow"></span>
        {#if text !== orig}<span class="badge amber">edited</span><Btn size="sm" variant="ghost" onclick={() => (text = orig)}>Revert</Btn>{/if}
        {#if !readOnly}<Btn size="sm" variant="primary" icon="save" busy={saving} disabled={text === orig || isTrunc(v.value)} onclick={saveString} data-testid="redis-save">Save</Btn>{/if}
      </div>
      <div class="sed">
        {#key `${v.key}:${fmt}`}
          <Editor value={text} lang={fmt === 'json' ? 'json' : 'text'} readonly={readOnly} onchange={(t) => (text = t)} onsave={saveString} height="100%" testid="redis-editor" ariaLabel="Value of {v.key}" />
        {/key}
      </div>
    {:else}
      <div class="mbar">
        {#if !readOnly}<Btn size="sm" icon="plus" onclick={openAdd} data-testid="redis-add-member">{type === 'list' ? 'Push' : type === 'stream' ? 'Add entry' : type === 'hash' ? 'Add field' : 'Add member'}</Btn>{/if}
        <span class="faint xs">{rows.length.toLocaleString()} shown{v.length !== null && v.length > rows.length ? ` of ${v.length.toLocaleString()}` : ''}</span>
      </div>
      <div class="mt table-wrap">
        <table class="table compact" data-testid="redis-members">
          <thead>
            <tr>
              {#if type === 'list'}<th class="ix">#</th><th>Value</th>{:else if type === 'hash'}<th>Field</th><th>Value</th>{:else if type === 'zset'}<th>Member</th><th class="r">Score</th>{:else if type === 'stream'}<th>Id</th><th>Fields</th>{:else}<th>Member</th>{/if}
              <th class="acts"></th>
            </tr>
          </thead>
          <tbody>
            {#each rows as r, i (i)}
              {@const pair = Array.isArray(r) ? (r as unknown[]) : null}
              <tr>
                {#if type === 'list'}
                  <td class="ix faint">{i}</td><td class="mono val" ondblclick={() => (viewVal = s(r))}>{s(r)}</td>
                {:else if type === 'hash' && pair}
                  <td class="mono k">{s(pair[0])}</td><td class="mono val" ondblclick={() => (viewVal = s(pair[1]))}>{s(pair[1])}</td>
                {:else if type === 'zset' && pair}
                  <td class="mono val">{s(pair[0])}</td><td class="mono r">{s(pair[1])}</td>
                {:else if type === 'stream'}
                  {@const e = r as { id: string; fields: unknown }}
                  <td class="mono k">{e.id}</td><td class="mono val">{JSON.stringify(e.fields)}</td>
                {:else}
                  <td class="mono val">{s(r)}</td>
                {/if}
                <td class="acts">
                  {#if !readOnly}
                    {#if type !== 'stream'}<button class="btn ghost icon xs" aria-label="Edit" title="Edit" onclick={() => (type === 'list' ? openEdit(s(r), '', i) : pair ? openEdit(s(pair[0]), s(pair[1])) : openEdit(s(r), ''))}><Icon name="edit" size={12} /></button>{/if}
                    <button class="btn ghost icon xs" aria-label="Remove" title="Remove" onclick={() => removeMember(type === 'stream' ? (r as { id: string }).id : pair ? s(pair[0]) : s(r))} data-testid="redis-rm-{i}"><Icon name="trash" size={12} /></button>
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      {#if v.next}<div class="more"><Btn size="sm" variant="ghost" icon="arrowDown" busy={loadingMore} onclick={more} data-testid="redis-value-more">Load more</Btn></div>{/if}
    {/if}
  {/if}
</div>

<Modal bind:open={ttlOpen} title="Expiry of {keyName}" size="sm" testid="redis-ttl-modal">
  <div class="field">
    <label for="ttl-in">Expire in (seconds)</label>
    <input id="ttl-in" class="input mono" type="number" min="1" bind:value={ttlVal} data-testid="redis-ttl-input" />
    <span class="hint">EXPIRE {keyName} n · or remove the expiry (PERSIST).</span>
  </div>
  {#snippet footer()}
    <Btn variant="ghost" onclick={() => setTtl(true)} data-testid="redis-persist">Remove expiry</Btn>
    <span class="grow"></span>
    <Btn variant="primary" onclick={() => setTtl(false)} data-testid="redis-ttl-set">Set TTL</Btn>
  {/snippet}
</Modal>
<Modal bind:open={renOpen} title="Rename {keyName}" size="sm">
  <div class="field">
    <label for="ren-in">New name</label>
    <input id="ren-in" class="input mono" bind:value={renVal} data-testid="redis-rename-input" onkeydown={(e) => e.key === 'Enter' && rename()} />
    <span class="hint">RENAMENX: refused when the new name exists.</span>
  </div>
  {#snippet footer()}
    <Btn variant="ghost" onclick={() => (renOpen = false)}>Cancel</Btn>
    <Btn variant="primary" onclick={rename} data-testid="redis-rename-ok">Rename</Btn>
  {/snippet}
</Modal>
<Confirm bind:open={delOpen} title="Delete key {keyName}?" confirmLabel="Delete" danger onconfirm={del} testid="redis-del-confirm">
  <p>DEL <span class="mono">{keyName}</span> — the value is gone for every client of this Redis.</p>
</Confirm>
<Modal bind:open={mem.open} title="{mem.mode === 'add' ? 'Add to' : 'Edit in'} {keyName}" size="sm" testid="redis-member-modal">
  <div class="stack">
    <div class="field">
      <label for="m-a">{memLabels[0]}</label>
      {#if type === 'list' || (type !== 'hash' && type !== 'zset' && type !== 'stream')}
        <textarea id="m-a" class="input mono" rows="4" bind:value={mem.a} data-testid="redis-member-a"></textarea>
      {:else}
        <input id="m-a" class="input mono" bind:value={mem.a} data-testid="redis-member-a" />
      {/if}
    </div>
    {#if memLabels[1]}
      <div class="field">
        <label for="m-b">{memLabels[1]}</label>
        {#if type === 'zset'}<input id="m-b" class="input mono" type="number" step="any" bind:value={mem.b} data-testid="redis-member-b" />{:else}<textarea id="m-b" class="input mono" rows="4" bind:value={mem.b} data-testid="redis-member-b"></textarea>{/if}
      </div>
    {/if}
  </div>
  {#snippet footer()}
    <Btn variant="ghost" onclick={() => (mem.open = false)}>Cancel</Btn>
    <Btn variant="primary" onclick={saveMember} data-testid="redis-member-ok">{mem.mode === 'add' ? 'Add' : 'Save'}</Btn>
  {/snippet}
</Modal>
<Modal open={viewVal !== null} title="Value" size="lg" onclose={() => (viewVal = null)}>
  <pre class="pre mono">{viewVal}</pre>
</Modal>

<style>
  .vp {
    display: flex;
    flex-direction: column;
    gap: 8px;
    height: 100%;
    min-height: 0;
    padding: 10px;
  }
  .vh {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
    min-width: 0;
  }
  .key {
    font-weight: var(--fw-semibold);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 50%;
  }
  .ttlb {
    all: unset;
    display: inline-flex;
    gap: 4px;
    align-items: center;
    font-size: var(--fs-xs);
    padding: 2px 8px;
    border-radius: var(--r-full);
    border: 1px solid var(--border);
    cursor: pointer;
    color: var(--text-2);
  }
  .ttlb.exp {
    color: var(--amber-text);
    background: var(--amber-soft);
    border-color: transparent;
  }
  .ttlb:disabled {
    cursor: default;
  }
  .strbar,
  .mbar {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
  }
  .sed {
    flex: 1;
    min-height: 200px;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    overflow: hidden;
  }
  .mt {
    flex: 1;
    min-height: 0;
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
  }
  .compact :global(td),
  .compact :global(th) {
    padding: 4px 10px;
    font-size: 12.5px;
  }
  .val {
    max-width: 520px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .k {
    white-space: nowrap;
    color: var(--violet-text);
  }
  .ix {
    width: 48px;
  }
  .r {
    text-align: right;
  }
  .acts {
    width: 64px;
    white-space: nowrap;
    text-align: right;
  }
  .more {
    display: flex;
    justify-content: center;
  }
  .pre {
    margin: 0;
    max-height: 60vh;
    overflow: auto;
    white-space: pre-wrap;
    word-break: break-word;
    font-size: 12.5px;
  }
</style>
