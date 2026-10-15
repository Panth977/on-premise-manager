<script lang="ts">
  /**
   * redis-cli console (§21 #97): one command per line (shell-like quoting), history with ArrowUp / ArrowDown (kept per
   * site + connection in localStorage, lines with credentials never), replies rendered by RESP type, blocked commands
   * explained, destructive ones through the typed-confirm modal (the workbench's exec).
   */
  import { tick } from 'svelte';
  import { parseRedisCommandLine, type RedisReply } from '@opm/shared/devtools';
  import Icon from '$lib/ui/Icon.svelte';
  import { loadJson, saveJson } from '../kinds';
  import { copyText } from '../grid';
  import { replyText, sensitiveLine } from './redis';
  import ReplyView from './ReplyView.svelte';

  interface Entry {
    id: number;
    line: string;
    reply: RedisReply | null;
    error: string | null;
    ms: number | null;
    note?: string;
  }
  interface Props {
    site: string;
    connectionId: string;
    db: string;
    /** run a line → reply / error (confirm flow inside) */
    run: (argv: string[]) => Promise<{ reply: RedisReply | null; error: string | null; ms: number | null; cancelled?: boolean }>;
  }
  let { site, connectionId, db, run }: Props = $props();
  // svelte-ignore state_referenced_locally
  const HKEY = `opm.dev.redis.hist.${site}.${connectionId}`;
  let hist = $state<string[]>(loadJson<string[]>(HKEY, []));
  let hpos = $state(-1);
  let line = $state('');
  let draft = '';
  let out = $state<Entry[]>([]);
  let busy = $state(false);
  let seq = 0;
  let box: HTMLDivElement | undefined = $state();
  let input: HTMLInputElement | undefined = $state();
  async function submit() {
    const l = line.trim();
    if (!l || busy) return;
    line = '';
    hpos = -1;
    if (!sensitiveLine(l)) {
      hist = [...hist.filter((h) => h !== l), l].slice(-200);
      saveJson(HKEY, hist);
    }
    if (/^(clear|cls)$/i.test(l)) {
      out = [];
      return;
    }
    if (/^help$/i.test(l)) {
      out = [...out, { id: ++seq, line: l, reply: null, error: null, ms: null, note: 'Type any Redis command, e.g. GET key · HGETALL user:1 · SCAN 0 MATCH user:* COUNT 100 · TTL key · INFO memory. Quotes: "a b" or \'a b\' (\\n \\t \\xHH in double quotes). ↑ / ↓ history · clear. Blocked: MONITOR, SUBSCRIBE…, SHUTDOWN, DEBUG, SELECT (the db is the connection’s). Destructive (FLUSHDB, FLUSHALL, CONFIG SET, DEL with a pattern-like key, EVAL) ask for the connection name.' }];
      return;
    }
    const argv = parseRedisCommandLine(l);
    if (!argv) {
      out = [...out, { id: ++seq, line: l, reply: null, error: 'Invalid argument(s): unbalanced quotes', ms: null }];
      return;
    }
    if (!argv.length) return;
    busy = true;
    const r = await run(argv);
    busy = false;
    out = [...out.slice(-300), { id: ++seq, line: l, reply: r.reply, error: r.error, ms: r.ms, note: r.cancelled ? 'not run (not confirmed)' : undefined }];
    await tick();
    box?.scrollTo({ top: box.scrollHeight });
    input?.focus();
  }
  function key(e: KeyboardEvent) {
    if (e.key === 'Enter') {
      e.preventDefault();
      submit();
    } else if (e.key === 'ArrowUp') {
      if (!hist.length) return;
      e.preventDefault();
      if (hpos === -1) draft = line;
      hpos = hpos === -1 ? hist.length - 1 : Math.max(0, hpos - 1);
      line = hist[hpos]!;
    } else if (e.key === 'ArrowDown') {
      if (hpos === -1) return;
      e.preventDefault();
      hpos = hpos + 1 >= hist.length ? -1 : hpos + 1;
      line = hpos === -1 ? draft : hist[hpos]!;
    } else if (e.key === 'l' && e.ctrlKey) {
      e.preventDefault();
      out = [];
    }
  }
</script>

<div class="con" data-testid="redis-console">
  <div class="out" bind:this={box} aria-live="polite">
    {#if !out.length}<p class="faint small hint0">redis-cli on the site server · db {db} · type <span class="mono">help</span>.</p>{/if}
    {#each out as e (e.id)}
      <div class="ent" data-testid="redis-out">
        <div class="cmd mono"><span class="pr">{db !== '0' ? `[${db}]` : ''}&gt;</span> {e.line}<span class="grow"></span>{#if e.ms !== null}<span class="ms">{e.ms} ms</span>{/if}<button class="cp" onclick={() => copyText(e.error ?? replyText(e.reply))} aria-label="Copy the reply" title="Copy"><Icon name="copy" size={11} /></button></div>
        {#if e.note}<div class="note">{e.note}</div>{/if}
        {#if e.error}<div class="err mono">(error) {e.error}</div>{:else if e.reply}<div class="rep"><ReplyView reply={e.reply} /></div>{/if}
      </div>
    {/each}
  </div>
  <div class="in">
    <span class="pr mono">{db !== '0' ? `[${db}]` : ''}&gt;</span>
    <input bind:this={input} class="mono" bind:value={line} onkeydown={key} placeholder="GET key · HGETALL user:1 · TTL key — ↑↓ history" spellcheck="false" autocomplete="off" aria-label="Redis command" data-testid="redis-cli" disabled={busy} />
    {#if busy}<span class="spin"></span>{/if}
  </div>
</div>

<style>
  .con {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
    background: var(--log-bg);
    color: var(--log-text);
    border-radius: var(--r-md);
    overflow: hidden;
  }
  .out {
    flex: 1;
    overflow: auto;
    padding: 10px 12px;
    font-size: 12.5px;
    min-height: 0;
  }
  .hint0 {
    color: #9696a2;
    margin: 0;
  }
  .ent {
    margin-bottom: 8px;
  }
  .cmd {
    display: flex;
    gap: 6px;
    align-items: center;
    color: #e4e4e7;
  }
  .pr {
    color: #a5b4fc;
  }
  .ms {
    color: #9696a2;
    font-size: 11px;
  }
  .cp {
    all: unset;
    cursor: pointer;
    color: #9696a2;
    display: inline-flex;
    opacity: 0;
  }
  .ent:hover .cp {
    opacity: 1;
  }
  .err {
    color: #f87171;
    white-space: pre-wrap;
  }
  .note {
    color: #fbbf24;
    font-size: 12px;
    white-space: pre-wrap;
  }
  .rep {
    padding-left: 14px;
  }
  .in {
    display: flex;
    gap: 6px;
    align-items: center;
    padding: 8px 12px;
    border-top: 1px solid #2a2a33;
  }
  .in input {
    flex: 1;
    background: transparent;
    border: 0;
    outline: 0;
    color: #fafafa;
    font-size: 13px;
    min-width: 0;
  }
  .in input::placeholder {
    color: #71717a;
  }
</style>
