<script lang="ts">
  /**
   * MQTT client (§21 #97): live session (mqttSession → EventSource on the ticketed streamPath, opened at once; a new
   * session when the stream ends unexpectedly), many filters with QoS (subscribe / unsubscribe on the running
   * session), live messages (pause, clear, filter, retained only, ≤ 5000 kept), topic tree split on "/" with counts +
   * latest payload, payload as text / JSON / hex / base64, retained · dup · qos badges, dropped indicator, topics seen,
   * publish (utf8 / json / base64 / hex, qos, retain; clearing a retained message asks the connection name).
   */
  import { onDestroy, onMount, tick, untrack } from 'svelte';
  import { session } from '$lib/state/session.svelte';
  import type { DevToolConnection, DevToolsInfo, MqttMessage, MqttPayloadEncoding, MqttSubscription, MqttTopicSeen, SavedMqttContent } from '@opm/shared/devtools';
  import { errMessage } from '$lib/api';
  import { toasts } from '$lib/state/toast.svelte';
  import { fmtClock, bytes } from '$lib/format';
  import Icon from '$lib/ui/Icon.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import Combobox from '$lib/ui/Combobox.svelte';
  import Editor from '../cm/Editor.svelte';
  import TypedConfirm from '../TypedConfirm.svelte';
  import SavedPanel from '../SavedPanel.svelte';
  import SaveDialog from '../SaveDialog.svelte';
  import { confirmReasons, dev, openMqttStream } from '../api';
  import { devStore } from '../state.svelte';
  import { copyText, hexDump } from '../grid';
  import { loadJson, saveJson } from '../kinds';
  import { isJsonText } from '../redis/redis';

  let { conn, site }: { conn: DevToolConnection; info: DevToolsInfo; site: string } = $props();
  const MAX = 5000;
  // svelte-ignore state_referenced_locally
  const SKEY = `opm.dev.mqtt.subs.${site}.${conn.id}`;

  // ── subscriptions + session
  let subs = $state<MqttSubscription[]>(loadJson<MqttSubscription[]>(SKEY, [{ filter: '#', qos: 0 }]));
  let newFilter = $state('');
  let newQos = $state<0 | 1 | 2>(0);
  type Status = 'idle' | 'connecting' | 'live' | 'reconnecting' | 'error';
  let status = $state<Status>('idle');
  let statusMsg = $state<string | null>(null);
  let sessionId = $state<string | null>(null);
  let stream: { close(): void } | null = null;
  let retries = 0;
  let retryT: ReturnType<typeof setTimeout> | null = null;
  let stopped = false;
  let dropped = $state(0);

  async function start() {
    if (!subs.length) return toasts.info('Add a topic filter first');
    stopped = false;
    status = retries ? 'reconnecting' : 'connecting';
    try {
      const s = await dev.mqttSession({ siteSlug: site, connectionId: conn.id, body: { subscriptions: subs } });
      sessionId = s.sessionId;
      // §7.6: a session whose stream is not opened within 2 min is closed — open it right away
      stream = openMqttStream(s.streamPath, {
        meta: (m) => (subs = m.subscriptions),
        message: (m) => queue(m),
        status: (st) => ((status = st.connected ? 'live' : 'reconnecting'), (statusMsg = st.error)),
        dropped: (n) => (dropped += n),
        end: (reason, unexpected) => ended(reason, unexpected),
      });
      status = 'live';
      statusMsg = null;
      retries = 0;
      void devStore.refreshConns();
    } catch (e) {
      status = 'error';
      statusMsg = errMessage(e);
      sessionId = null;
    }
  }
  function ended(reason: string, unexpected: boolean) {
    stream = null;
    sessionId = null;
    if (stopped) return;
    if (!unexpected && reason === 'closed by the operator') {
      status = 'idle';
      return;
    }
    // unexpected end (network, broker lost, agent restart, 6 h limit) → a new session
    retries++;
    if (retries > 5) {
      status = 'error';
      statusMsg = `The stream ended (${reason}); 5 reconnects failed.`;
      return;
    }
    status = 'reconnecting';
    statusMsg = `stream ended (${reason}) — reconnecting (${retries}/5)…`;
    retryT = setTimeout(start, Math.min(8000, 1000 * retries));
  }
  async function stop() {
    stopped = true;
    if (retryT) clearTimeout(retryT);
    const id = sessionId;
    stream?.close();
    stream = null;
    sessionId = null;
    status = 'idle';
    if (id) await dev.mqttCloseSession({ siteSlug: site, connectionId: conn.id, sessionId: id }).catch(() => {});
  }
  onDestroy(() => {
    stopped = true;
    if (retryT) clearTimeout(retryT);
    stream?.close();
    if (sessionId) void dev.mqttCloseSession({ siteSlug: site, connectionId: conn.id, sessionId }).catch(() => {});
  });
  async function addSub() {
    const f = newFilter.trim();
    if (!f) return;
    if (/#./.test(f) || /[^/]#|#[^/]/.test(f.replace(/^#$/, ''))) return toasts.push('bad', 'Invalid filter', '"#" must be the last level, alone (e.g. sensors/#).');
    newFilter = '';
    const s = { filter: f, qos: newQos };
    if (sessionId) {
      try {
        const r = await dev.mqttSubscribe({ siteSlug: site, connectionId: conn.id, sessionId, body: s });
        subs = r.subscriptions;
      } catch (e) {
        return toasts.err(e, 'Subscribe failed');
      }
    } else subs = [...subs.filter((x) => x.filter !== f), s];
    saveJson(SKEY, subs);
    if (!sessionId && status !== 'connecting') start();
  }
  async function removeSub(f: string) {
    if (sessionId) {
      try {
        const r = await dev.mqttUnsubscribe({ siteSlug: site, connectionId: conn.id, sessionId, body: { filter: f } });
        subs = r.subscriptions;
      } catch (e) {
        return toasts.err(e, 'Unsubscribe failed');
      }
    } else subs = subs.filter((x) => x.filter !== f);
    saveJson(SKEY, subs);
    if (!subs.length) stop();
  }

  // ── messages (raw arrays, flushed once per frame)
  let msgs = $state.raw<MqttMessage[]>([]);
  let pending: MqttMessage[] = [];
  let raf = 0;
  let paused = $state(false);
  let pausedCount = $state(0);
  let topicStats = $state.raw<Map<string, { count: number; last: MqttMessage }>>(new Map());
  function queue(m: MqttMessage) {
    pending.push(m);
    if (!raf) raf = requestAnimationFrame(flush);
  }
  function flush() {
    raf = 0;
    const batch = pending;
    pending = [];
    const st = new Map(topicStats);
    for (const m of batch) st.set(m.topic, { count: (st.get(m.topic)?.count ?? 0) + 1, last: m });
    topicStats = st;
    if (paused) {
      pausedCount += batch.length;
      held.push(...batch);
      return;
    }
    const n = msgs.concat(batch);
    msgs = n.length > MAX ? n.slice(n.length - MAX) : n;
    if (follow) void tick().then(() => list?.scrollTo({ top: list.scrollHeight }));
  }
  let held: MqttMessage[] = [];
  function togglePause() {
    paused = !paused;
    if (!paused && held.length) {
      const n = msgs.concat(held);
      msgs = n.length > MAX ? n.slice(n.length - MAX) : n;
      held = [];
      pausedCount = 0;
    }
  }
  function clear() {
    msgs = [];
    held = [];
    pausedCount = 0;
    sel = null;
    dropped = 0;
  }
  let q = $state('');
  let retainedOnly = $state(false);
  let topicSel = $state<string | null>(null);
  const shown = $derived.by(() => {
    const t = q.trim().toLowerCase();
    return msgs.filter((m) => (!retainedOnly || m.retained) && (!topicSel || m.topic === topicSel || m.topic.startsWith(topicSel + '/')) && (!t || m.topic.toLowerCase().includes(t) || (m.text ?? '').toLowerCase().includes(t)));
  });
  const VISIBLE = 400;
  const tail = $derived(shown.slice(-VISIBLE));
  let list: HTMLDivElement | undefined = $state();
  let follow = $state(true);
  let sel = $state<MqttMessage | null>(null);

  // ── topics seen (agent) + tree
  let seen = $state<MqttTopicSeen[]>([]);
  async function loadSeen() {
    try {
      seen = (await dev.mqttTopics({ siteSlug: site, connectionId: conn.id })).items;
    } catch {
      seen = [];
    }
  }
  interface TNode {
    name: string;
    path: string;
    kids: Map<string, TNode>;
    count: number;
    last: MqttMessage | null;
    seen: MqttTopicSeen | null;
  }
  const tree = $derived.by(() => {
    const root: TNode = { name: '', path: '', kids: new Map(), count: 0, last: null, seen: null };
    const add = (topic: string, count: number, last: MqttMessage | null, s: MqttTopicSeen | null) => {
      let n = root;
      const parts = topic.split('/');
      parts.forEach((p, i) => {
        let c = n.kids.get(p);
        if (!c) {
          c = { name: p === '' ? '(empty)' : p, path: parts.slice(0, i + 1).join('/'), kids: new Map(), count: 0, last: null, seen: null };
          n.kids.set(p, c);
        }
        c.count += count;
        n = c;
      });
      if (last && (!n.last || n.last.seq < last.seq)) n.last = last;
      if (s) n.seen = s;
    };
    for (const [t, v] of topicStats) add(t, v.count, v.last, null);
    for (const s of seen) if (!topicStats.has(s.topic)) add(s.topic, 0, null, s);
    return root;
  });
  let open = $state<Record<string, boolean>>({});
  const kidsOf = (n: TNode) => [...n.kids.values()].sort((a, b) => a.name.localeCompare(b.name, undefined, { numeric: true }));

  // ── payload views
  type View = 'text' | 'json' | 'hex' | 'base64';
  let view = $state<View>('json');
  function bytesOf(m: MqttMessage): Uint8Array {
    if (m.text !== null) return new TextEncoder().encode(m.text);
    const bin = atob(m.base64 ?? '');
    const out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out;
  }
  function b64Of(m: MqttMessage) {
    if (m.base64 !== null) return m.base64;
    let s = '';
    for (const b of bytesOf(m)) s += String.fromCharCode(b);
    return btoa(s);
  }
  function payloadText(m: MqttMessage, v: View): string {
    if (v === 'hex') return hexDump(bytesOf(m));
    if (v === 'base64') return b64Of(m);
    if (m.text === null) return `(binary, ${m.sizeBytes} bytes — view as hex / base64)`;
    if (v === 'json' && isJsonText(m.text)) return JSON.stringify(JSON.parse(m.text), null, 2);
    return m.text;
  }
  const preview = (m: MqttMessage | null) => (!m ? '' : m.text !== null ? m.text.replace(/\s+/g, ' ').slice(0, 120) : `binary ${m.sizeBytes} B`);

  // ── publish
  let pub = $state<{ topic: string; payload: string; encoding: MqttPayloadEncoding; qos: 0 | 1 | 2; retain: boolean }>({ topic: '', payload: '', encoding: 'json', qos: 0, retain: false });
  let publishing = $state(false);
  let confirm = $state<{ open: boolean; reasons: string[]; what: string; again: (t: string) => void }>({ open: false, reasons: [], what: '', again: () => {} });
  async function publish(body = pub, typed?: string) {
    if (!body.topic.trim()) return toasts.push('bad', 'Topic required');
    publishing = true;
    try {
      const r = await dev.mqttPublish({ siteSlug: site, connectionId: conn.id, body: { ...body, topic: body.topic.trim(), confirm: typed } });
      toasts.ok(`Published ${r.bytes} bytes to ${body.topic}`, body.retain ? (r.bytes ? 'retained' : 'retained message cleared') : undefined);
      void loadSeen();
    } catch (e) {
      const reasons = confirmReasons(e);
      if (reasons && typed === undefined) confirm = { open: true, reasons, what: `PUBLISH ${body.topic} (empty payload, retain)`, again: (t) => publish(body, t) };
      else toasts.err(e, 'Publish failed');
    } finally {
      publishing = false;
    }
  }
  function clearRetained(topic: string) {
    publish({ topic, payload: '', encoding: 'utf8', qos: 1, retain: true });
  }
  const topicOpts = $derived([...new Set([...topicStats.keys(), ...seen.map((s) => s.topic)])].sort().map((t) => ({ value: t })));
  let saveOpen = $state(false);
  let savedV = $state(0);
  let showSaved = $state(false);

  // start once the person is declared (a full page load asks first; the session call needs the actor)
  let autoStarted = false;
  onMount(() => void loadSeen());
  $effect(() => {
    if (session.actor && !autoStarted) {
      autoStarted = true;
      untrack(() => start());
    }
  });
  const live = $derived(status === 'live');
</script>

{#snippet tnode(n: TNode, depth: number)}
  {#each kidsOf(n) as c (c.path)}
    {@const o = open[c.path] ?? depth < 1}
    <div class="tn" class:on={topicSel === c.path} style:padding-left="{6 + depth * 12}px">
      {#if c.kids.size}<button class="tg" onclick={() => (open = { ...open, [c.path]: !o })} aria-label={o ? 'Collapse' : 'Expand'} aria-expanded={o}><Icon name={o ? 'chevronDown' : 'chevronRight'} size={11} /></button>{:else}<span class="tg"></span>{/if}
      <button class="tl" onclick={() => (topicSel = topicSel === c.path ? null : c.path)} title="{c.path} — click: show only this topic" data-testid="mt-{c.path}">
        <span class="nm mono">{c.name}</span>
        {#if c.last?.retained || c.seen?.retained}<span class="rb" title="retained">R</span>{/if}
        <span class="cnt">{c.count || (c.seen ? `seen ${c.seen.messages}` : '')}</span>
      </button>
    </div>
    {#if c.last || c.seen}<div class="lp mono" style:padding-left="{30 + depth * 12}px">{c.last ? preview(c.last) : (c.seen?.lastPreview ?? '')}</div>{/if}
    {#if o && c.kids.size}{@render tnode(c, depth + 1)}{/if}
  {/each}
{/snippet}

<div class="mq" data-testid="mqtt-client">
  <aside class="col left">
    <div class="sh">
      <span class="badge {live ? 'green' : status === 'error' ? 'red' : status === 'idle' ? 'outline' : 'amber'}" data-testid="mqtt-status"><i class="dot"></i>{status === 'live' ? 'live' : status}</span>
      {#if sessionId}<span class="faint xs mono" title="session id">{sessionId.slice(0, 10)}…</span>{/if}
      <span class="grow"></span>
      {#if sessionId || status === 'reconnecting'}<Btn size="xs" variant="ghost" icon="stop" onclick={stop} data-testid="mqtt-stop">Stop</Btn>{:else}<Btn size="xs" icon="play" onclick={() => ((retries = 0), start())} data-testid="mqtt-start">Start</Btn>{/if}
    </div>
    {#if statusMsg}<div class="smsg small {status === 'error' ? 'bad-text' : 'warn-text'}" data-testid="mqtt-status-msg">{statusMsg}</div>{/if}
    <div class="subs" data-testid="mqtt-subs">
      <div class="lbl">Subscriptions</div>
      {#each subs as s (s.filter)}
        <div class="sub"><span class="mono grow">{s.filter}</span><span class="badge outline">qos {s.qos}</span><button class="btn ghost icon xs" onclick={() => removeSub(s.filter)} aria-label="Unsubscribe {s.filter}" data-testid="mqtt-unsub-{s.filter}"><Icon name="x" size={12} /></button></div>
      {:else}
        <p class="faint xs">No filter — add one (e.g. <span class="mono">sensors/#</span>, <span class="mono">+/status</span>).</p>
      {/each}
      <div class="addsub">
        <input class="input sm mono grow" bind:value={newFilter} placeholder="topic/filter/#" onkeydown={(e) => e.key === 'Enter' && addSub()} aria-label="Topic filter" data-testid="mqtt-filter" />
        <SegmentedControl options={[{ value: 0 as const, label: '0' }, { value: 1 as const, label: '1' }, { value: 2 as const, label: '2' }]} value={newQos} onchange={(v) => (newQos = v)} label="QoS" size="sm" />
        <Btn size="sm" icon="plus" onclick={addSub} data-testid="mqtt-sub">Add</Btn>
      </div>
    </div>
    <div class="treeh"><span class="lbl">Topics</span><span class="grow"></span>{#if topicSel}<button class="btn ghost xs" onclick={() => (topicSel = null)}>all topics</button>{/if}<button class="btn ghost icon xs" onclick={loadSeen} aria-label="Refresh topics seen" title="Topics seen by this connection on the site (mqttTopics)"><Icon name="refresh" size={12} /></button></div>
    <div class="tree" data-testid="mqtt-tree">
      {@render tnode(tree, 0)}
      {#if !tree.kids.size}<p class="faint xs pad">No topic yet.</p>{/if}
    </div>
  </aside>

  <section class="col mid">
    <div class="mh">
      <Btn size="sm" variant={paused ? 'primary' : 'default'} icon={paused ? 'play' : 'pause'} onclick={togglePause} data-testid="mqtt-pause">{paused ? `Resume${pausedCount ? ` (${pausedCount})` : ''}` : 'Pause'}</Btn>
      <Btn size="sm" variant="ghost" icon="eraser" onclick={clear} data-testid="mqtt-clear">Clear</Btn>
      <input class="input sm filt" bind:value={q} placeholder="Filter topic / payload…" aria-label="Filter messages" data-testid="mqtt-q" />
      <label class="switch small"><input type="checkbox" bind:checked={retainedOnly} data-testid="mqtt-retained-only" />Retained</label>
      <span class="grow"></span>
      {#if dropped}<span class="badge amber" title="The agent dropped messages because the browser was too slow (slow consumer)" data-testid="mqtt-dropped"><Icon name="alert" size={11} />{dropped} dropped</span>{/if}
      <span class="faint xs nowrap" data-testid="mqtt-count">{shown.length.toLocaleString()} / {msgs.length.toLocaleString()} (max {MAX})</span>
    </div>
    {#if topicSel}<div class="tsel small">Showing <span class="mono">{topicSel}</span> and below</div>{/if}
    <div class="msgs" bind:this={list} onscroll={(e) => (follow = e.currentTarget.scrollTop + e.currentTarget.clientHeight >= e.currentTarget.scrollHeight - 30)} data-testid="mqtt-messages">
      {#if shown.length > VISIBLE}<div class="faint xs pad">… {(shown.length - VISIBLE).toLocaleString()} older messages kept (filter or clear to see them)</div>{/if}
      {#each tail as m (m.seq)}
        <button class="msg" class:on={sel?.seq === m.seq} onclick={() => (sel = m)} data-testid="mqtt-msg">
          <span class="mt1"><span class="tm mono">{fmtClock(m.at)}</span><span class="tp mono">{m.topic}</span>{#if m.retained}<span class="b r" title="retained">retained</span>{/if}{#if m.duplicate}<span class="b d">dup</span>{/if}<span class="b q">qos {m.qos}</span><span class="sz">{bytes(m.sizeBytes)}</span></span>
          <span class="pv mono">{preview(m)}</span>
        </button>
      {:else}
        <div class="faint small pad">{status === 'live' ? 'Waiting for messages…' : 'Start the session to receive messages.'}</div>
      {/each}
    </div>
    {#if !follow}<button class="jump btn sm" onclick={() => ((follow = true), list?.scrollTo({ top: list.scrollHeight }))}><Icon name="toBottom" size={13} />Latest</button>{/if}
  </section>

  <aside class="col rcol">
    <div class="detail" data-testid="mqtt-detail">
      {#if sel}
        <div class="dh">
          <span class="mono strong tp2" title={sel.topic}>{sel.topic}</span>
          <button class="btn ghost icon xs" onclick={async () => (await copyText(sel!.topic)) && toasts.ok('Topic copied')} aria-label="Copy topic"><Icon name="copy" size={12} /></button>
        </div>
        <div class="faint xs">{new Date(sel.at).toLocaleString()} · qos {sel.qos}{sel.retained ? ' · retained' : ''}{sel.duplicate ? ' · dup' : ''} · {sel.sizeBytes} bytes{sel.truncated ? ' · cut' : ''}</div>
        <div class="row gap-s">
          <SegmentedControl options={[{ value: 'json' as View, label: 'JSON' }, { value: 'text' as View, label: 'Text' }, { value: 'hex' as View, label: 'Hex' }, { value: 'base64' as View, label: 'Base64' }]} value={view} onchange={(v) => (view = v)} label="Payload view" size="sm" testid="mqtt-view" />
          <span class="grow"></span>
          <Btn size="xs" variant="ghost" icon="copy" onclick={() => copyText(payloadText(sel!, view))}>Copy</Btn>
        </div>
        <pre class="pl mono" data-testid="mqtt-payload">{payloadText(sel, view)}</pre>
        <div class="row gap-s">
          <Btn size="xs" variant="ghost" icon="send" onclick={() => (pub = { ...pub, topic: sel!.topic, payload: sel!.text ?? b64Of(sel!), encoding: sel!.text === null ? 'base64' : isJsonText(sel!.text) ? 'json' : 'utf8' })}>Copy to publish</Btn>
          {#if sel.retained && !conn.readOnly}<Btn size="xs" variant="ghost" icon="eraser" onclick={() => clearRetained(sel!.topic)} data-testid="mqtt-clear-retained">Clear retained</Btn>{/if}
        </div>
      {:else}
        <p class="faint small pad">Select a message to see its payload.</p>
      {/if}
    </div>
    <div class="pub" data-testid="mqtt-publish">
      <div class="ph"><span class="lbl">Publish</span><span class="grow"></span><button class="btn ghost xs" onclick={() => (showSaved = !showSaved)} aria-pressed={showSaved}><Icon name="save" size={12} />Saved</button></div>
      {#if showSaved}
        <div class="saved"><SavedPanel {site} kinds={['mqtt']} connectionId={conn.id} version={savedV} preview={(s) => { try { return (JSON.parse(s.content) as SavedMqttContent).topic; } catch { return s.content; } }} onopen={(s) => { try { pub = { ...pub, ...(JSON.parse(s.content) as SavedMqttContent) }; showSaved = false; } catch { toasts.push('bad', 'Not a saved MQTT message'); } }} /></div>
      {/if}
      {#if conn.readOnly}<div class="banner warn small"><Icon name="lock" size={14} />Read-only connection: publishing is refused.</div>{/if}
      <Combobox value={pub.topic} onchange={(v) => (pub.topic = v)} options={topicOpts} allowCustom placeholder="topic/to/publish" mono size="sm" testid="mqtt-pub-topic" label="Topic" />
      <div class="pe">
        {#key pub.encoding}<Editor value={pub.payload} lang={pub.encoding === 'json' ? 'json' : 'text'} onchange={(v) => (pub.payload = v)} onrun={() => publish()} lineNumbers={false} height="130px" placeholder={pub.encoding === 'hex' ? '01 02 fe ff' : pub.encoding === 'base64' ? 'AQL+/w==' : '{"hello":"world"}'} testid="mqtt-pub-payload" ariaLabel="Payload" />{/key}
      </div>
      <div class="row gap-s wrapr">
        <SegmentedControl options={[{ value: 'json' as MqttPayloadEncoding, label: 'JSON' }, { value: 'utf8' as MqttPayloadEncoding, label: 'Text' }, { value: 'base64' as MqttPayloadEncoding, label: 'Base64' }, { value: 'hex' as MqttPayloadEncoding, label: 'Hex' }]} value={pub.encoding} onchange={(v) => (pub.encoding = v)} label="Encoding" size="sm" testid="mqtt-pub-enc" />
        <SegmentedControl options={[{ value: 0 as const, label: 'qos 0' }, { value: 1 as const, label: '1' }, { value: 2 as const, label: '2' }]} value={pub.qos} onchange={(v) => (pub.qos = v)} label="QoS" size="sm" />
        <label class="switch small"><input type="checkbox" bind:checked={pub.retain} data-testid="mqtt-pub-retain" />Retain</label>
      </div>
      {#if pub.retain && !pub.payload.trim()}<div class="small warn-text">An empty retained message CLEARS the retained message of this topic (asks for the connection name).</div>{/if}
      <div class="row gap-s">
        <Btn variant="ghost" size="sm" icon="save" onclick={() => (saveOpen = true)}>Save…</Btn>
        <span class="grow"></span>
        <Btn variant="primary" size="sm" icon="send" busy={publishing} disabled={conn.readOnly} onclick={() => publish()} data-testid="mqtt-pub-go">Publish</Btn>
      </div>
    </div>
  </aside>
</div>

<TypedConfirm bind:open={confirm.open} connectionName={conn.name} reasons={confirm.reasons} what={confirm.what} actionLabel="Clear it" onconfirm={(t) => confirm.again(t)} />
<SaveDialog bind:open={saveOpen} {site} kind="mqtt" connectionId={conn.id} content={JSON.stringify(pub satisfies SavedMqttContent)} suggestedName={pub.topic} onsaved={() => savedV++} />

<style>
  .mq {
    display: grid;
    grid-template-columns: minmax(260px, 300px) minmax(0, 1fr) minmax(300px, 380px);
    gap: 10px;
    height: calc(100dvh - 132px);
    min-height: 540px;
  }
  .col {
    border: 1px solid var(--border);
    border-radius: var(--r-lg);
    background: var(--surface);
    min-height: 0;
    min-width: 0;
    display: flex;
    flex-direction: column;
    overflow: hidden;
    position: relative;
  }
  .sh,
  .mh,
  .treeh,
  .ph,
  .dh {
    display: flex;
    gap: 6px;
    align-items: center;
    flex-wrap: wrap;
  }
  .sh {
    padding: 8px 10px;
    border-bottom: 1px solid var(--border);
  }
  .smsg {
    padding: 4px 10px;
  }
  .lbl {
    font-size: var(--fs-xs);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-3);
    font-weight: var(--fw-semibold);
  }
  .subs {
    padding: 8px 10px;
    display: flex;
    flex-direction: column;
    gap: 4px;
    border-bottom: 1px solid var(--border);
  }
  .sub {
    display: flex;
    gap: 6px;
    align-items: center;
    font-size: 12.5px;
  }
  .addsub {
    display: flex;
    gap: 4px;
    align-items: center;
    flex-wrap: wrap;
    margin-top: 4px;
  }
  .addsub .input {
    height: 28px;
    min-width: 100px;
  }
  .treeh {
    padding: 8px 10px 2px;
  }
  .tree {
    overflow: auto;
    flex: 1;
    padding: 0 4px 8px;
    font-size: 12.5px;
  }
  .tn {
    display: flex;
    align-items: center;
    border-radius: var(--r-sm);
  }
  .tn.on {
    background: var(--accent-soft);
  }
  .tg {
    all: unset;
    width: 16px;
    height: 20px;
    display: grid;
    place-items: center;
    cursor: pointer;
    color: var(--text-3);
    flex: none;
  }
  .tl {
    all: unset;
    flex: 1;
    display: flex;
    gap: 5px;
    align-items: center;
    padding: 2px 4px;
    cursor: pointer;
    min-width: 0;
  }
  .tl:hover {
    background: var(--surface-hover);
  }
  .nm {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .cnt {
    margin-left: auto;
    font-size: 11px;
    color: var(--text-3);
    white-space: nowrap;
  }
  .rb {
    font-size: 9px;
    font-weight: 700;
    color: var(--violet-text);
    background: var(--violet-soft);
    border-radius: 3px;
    padding: 0 3px;
  }
  .lp {
    font-size: 10.5px;
    color: var(--text-3);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    padding-right: 6px;
  }
  .mh {
    padding: 6px 8px;
    border-bottom: 1px solid var(--border);
  }
  .filt {
    width: 200px;
    height: 28px;
  }
  .tsel {
    padding: 4px 10px;
    background: var(--accent-soft);
  }
  .msgs {
    flex: 1;
    overflow: auto;
    min-height: 0;
  }
  .msg {
    all: unset;
    box-sizing: border-box;
    display: flex;
    flex-direction: column;
    gap: 1px;
    width: 100%;
    padding: 5px 10px;
    border-bottom: 1px solid color-mix(in srgb, var(--border) 60%, transparent);
    cursor: pointer;
    min-width: 0;
  }
  .msg:hover {
    background: var(--surface-hover);
  }
  .msg.on {
    background: var(--accent-soft);
  }
  .mt1 {
    display: flex;
    gap: 6px;
    align-items: center;
    font-size: 12px;
    min-width: 0;
  }
  .tm {
    color: var(--text-3);
    font-size: 11px;
  }
  .tp {
    color: var(--accent-text);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
  }
  .b {
    font-size: 10px;
    border-radius: 3px;
    padding: 0 4px;
    white-space: nowrap;
  }
  .b.r {
    background: var(--violet-soft);
    color: var(--violet-text);
  }
  .b.d {
    background: var(--amber-soft);
    color: var(--amber-text);
  }
  .b.q {
    background: var(--gray-soft);
    color: var(--text-2);
  }
  .sz {
    margin-left: auto;
    font-size: 10.5px;
    color: var(--text-3);
    white-space: nowrap;
  }
  .pv {
    font-size: 11.5px;
    color: var(--text-2);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .jump {
    position: absolute;
    bottom: 10px;
    right: 14px;
    box-shadow: var(--shadow-md);
  }
  .pad {
    padding: 10px;
    margin: 0;
  }
  .detail {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 10px;
    border-bottom: 1px solid var(--border);
    overflow: hidden;
  }
  .tp2 {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
    flex: 1;
  }
  .pl {
    flex: 1;
    min-height: 60px;
    margin: 0;
    overflow: auto;
    padding: 8px 10px;
    background: var(--code-bg);
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    font-size: 12px;
    white-space: pre-wrap;
    word-break: break-word;
  }
  .pub {
    padding: 10px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .saved {
    height: 200px;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
  }
  .pe {
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    overflow: hidden;
  }
  .wrapr {
    flex-wrap: wrap;
  }
  @media (max-width: 1100px) {
    .mq {
      grid-template-columns: minmax(240px, 280px) minmax(0, 1fr);
    }
    .rcol {
      grid-column: 1 / -1;
      flex-direction: row;
      flex-wrap: wrap;
    }
    .rcol > * {
      flex: 1 1 320px;
    }
    .mq {
      height: auto;
    }
    .col.left,
    .col.mid {
      height: 560px;
    }
  }
  @media (max-width: 700px) {
    .mq {
      grid-template-columns: minmax(0, 1fr);
    }
    .col.left {
      height: auto;
      max-height: 460px;
    }
    .col.mid {
      height: 460px;
    }
    .rcol {
      flex-direction: column;
    }
    .detail {
      min-height: 220px;
    }
  }
</style>
