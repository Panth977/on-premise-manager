<script lang="ts">
  /**
   * HTTP response (§21 #97): status (colour), time, size, protocol, final URL; tabs Body (pretty JSON with folding /
   * raw / preview of images + HTML in a sandboxed srcdoc iframe / hex), Headers, Cookies, Timing (bars), Redirects,
   * Request as sent; "Download full body" (resultBody) when cut or binary.
   */
  import type { ExecuteHttpResponse } from '@opm/shared/devtools';
  import { bytes } from '$lib/format';
  import { toasts } from '$lib/state/toast.svelte';
  import { downloadUrl } from '$lib/tools/api';
  import Icon from '$lib/ui/Icon.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import Editor from '../cm/Editor.svelte';
  import { devPath } from '../api';
  import { copyText, hexDump } from '../grid';
  import { isJsonText } from '../redis/redis';
  import { b64ToBytes, statusTone } from './http';

  let { res, site }: { res: ExecuteHttpResponse; site: string } = $props();
  type Tab = 'body' | 'headers' | 'cookies' | 'timing' | 'redirects' | 'sent';
  let tab = $state<Tab>('body');
  const ct = $derived((res.contentType ?? '').toLowerCase());
  const isJson = $derived(ct.includes('json') || (res.bodyText !== null && isJsonText(res.bodyText)));
  const isHtml = $derived(ct.includes('text/html'));
  const isImg = $derived(ct.startsWith('image/'));
  type BodyView = 'pretty' | 'raw' | 'preview' | 'hex';
  // svelte-ignore state_referenced_locally
  let bv = $state<BodyView>(isImg || isHtml ? 'preview' : 'pretty');
  const pretty = $derived.by(() => {
    if (res.bodyText === null) return '';
    if (!isJson) return res.bodyText;
    try {
      return JSON.stringify(JSON.parse(res.bodyText), null, 2);
    } catch {
      return res.bodyText;
    }
  });
  const bodyBytes = $derived(res.bodyBase64 !== null ? b64ToBytes(res.bodyBase64) : new TextEncoder().encode(res.bodyText ?? ''));
  const bodyUrl = $derived(res.resultId ? devPath('resultBody', { siteSlug: site, resultId: res.resultId }) : null);
  const t = $derived(res.timing);
  const segs = $derived(
    [
      ['DNS', t.dnsMs, 'blue'],
      ['Connect', t.connectMs, 'violet'],
      ['TLS', t.tlsMs, 'amber'],
      ['Waiting (TTFB)', t.ttfbMs, 'green'],
      ['Download', t.downloadMs, 'accent'],
    ].filter(([, v]) => v !== null) as [string, number, string][],
  );
  const segTotal = $derived(Math.max(t.totalMs, segs.reduce((a, [, v]) => a + v, 0)));
  async function copyBody() {
    if (await copyText(bv === 'pretty' ? pretty : (res.bodyText ?? res.bodyBase64 ?? ''))) toasts.ok('Body copied');
  }
</script>

<div class="rv" data-testid="http-response">
  <div class="sl">
    {#if res.ok}
      <span class="badge {statusTone(res.status)} st" data-testid="http-status">{res.status} {res.statusText ?? ''}</span>
    {:else}
      <span class="badge red st" data-testid="http-status">Failed</span>
    {/if}
    <span class="m"><Icon name="timer" size={12} />{t.totalMs} ms</span>
    <span class="m"><Icon name="download" size={12} />{bytes(res.sizeBytes)}</span>
    {#if res.protocol}<span class="m">{res.protocol}</span>{/if}
    {#if res.redirects.length}<span class="m">{res.redirects.length} redirect{res.redirects.length === 1 ? '' : 's'}</span>{/if}
    <span class="grow"></span>
    {#if res.finalUrl}<span class="fu mono faint xs" title={res.finalUrl}>{res.finalUrl}</span>{/if}
  </div>
  {#if !res.ok}
    <div class="banner bad"><Icon name="alert" /><div class="grow">{res.error}</div></div>
  {/if}
  <div class="tabs" role="tablist">
    {#each [['body', 'Body'], ['headers', `Headers ${res.headers.length}`], ['cookies', `Cookies ${res.cookies.length}`], ['timing', 'Timing'], ['redirects', `Redirects ${res.redirects.length}`], ['sent', 'Request']] as [k, l] (k)}
      <button role="tab" aria-selected={tab === k} class:active={tab === k} onclick={() => (tab = k as Tab)} data-testid="http-tab-{k}">{l}</button>
    {/each}
  </div>
  <div class="tb">
    {#if tab === 'body'}
      <div class="bbar">
        <SegmentedControl options={[{ value: 'pretty' as BodyView, label: isJson ? 'Pretty' : 'Text', disabled: res.bodyText === null }, { value: 'raw' as BodyView, label: 'Raw', disabled: res.bodyText === null }, { value: 'preview' as BodyView, label: 'Preview', disabled: !isHtml && !isImg }, { value: 'hex' as BodyView, label: 'Hex' }]} value={bv} onchange={(v) => (bv = v)} label="Body view" size="sm" testid="http-body-view" />
        {#if res.bodyTruncated}<span class="badge amber" title="Only the first part is inline">cut at {bytes(res.bodyText?.length ?? 0)}</span>{/if}
        <span class="grow"></span>
        {#if bodyUrl}<Btn size="xs" icon="download" onclick={() => downloadUrl(bodyUrl!, 'response-body')} data-testid="http-full-body">Download full body</Btn>{/if}
        <Btn size="xs" variant="ghost" icon="copy" onclick={copyBody}>Copy</Btn>
      </div>
      <div class="body">
        {#if res.sizeBytes === 0}
          <p class="faint small pad">(empty body)</p>
        {:else if res.bodyText === null && res.bodyBase64 === null}
          <p class="small pad" data-testid="http-body-big">The body ({bytes(res.sizeBytes)}) is larger than the inline limit — use “Download full body”.</p>
        {:else if bv === 'pretty' && res.bodyText !== null}
          {#key res.queryId}<Editor value={pretty} lang={isJson ? 'json' : 'text'} readonly height="100%" testid="http-body" ariaLabel="Response body" />{/key}
        {:else if bv === 'raw'}
          <pre class="pre mono" data-testid="http-raw">{res.bodyText}</pre>
        {:else if bv === 'preview' && isImg && res.bodyBase64}
          <div class="img"><img src="data:{ct.split(';')[0]};base64,{res.bodyBase64}" alt="response preview" data-testid="http-img" /></div>
        {:else if bv === 'preview' && isHtml}
          <iframe class="frame" title="HTML preview (sandboxed)" sandbox="allow-scripts" srcdoc={res.bodyText ?? ''} data-testid="http-iframe"></iframe>
        {:else}
          <pre class="pre mono">{hexDump(bodyBytes)}</pre>
        {/if}
      </div>
    {:else if tab === 'headers'}
      <table class="table compact kvt" data-testid="http-headers">
        <tbody>{#each res.headers as [k, v], i (i)}<tr><td class="mono k">{k}</td><td class="mono">{v}</td></tr>{/each}</tbody>
      </table>
    {:else if tab === 'cookies'}
      {#if res.cookies.length}
        <table class="table compact" data-testid="http-cookies">
          <thead><tr><th>Name</th><th>Value</th><th>Domain</th><th>Path</th><th>Expires</th><th>Flags</th></tr></thead>
          <tbody>
            {#each res.cookies as c (c.name)}
              <tr><td class="mono">{c.name}</td><td class="mono val">{c.value}</td><td class="mono">{c.domain ?? ''}</td><td class="mono">{c.path ?? ''}</td><td class="small">{c.expires ? new Date(c.expires).toLocaleString() : 'session'}</td><td class="small">{[c.httpOnly ? 'HttpOnly' : '', c.secure ? 'Secure' : ''].filter(Boolean).join(' · ')}</td></tr>
            {/each}
          </tbody>
        </table>
      {:else}<p class="faint small pad">No cookies set.</p>{/if}
    {:else if tab === 'timing'}
      <div class="timing" data-testid="http-timing">
        {#each segs as [name, v, tone], i (name)}
          {@const before = segs.slice(0, i).reduce((a, [, x]) => a + x, 0)}
          <div class="tr"><span class="small">{name}</span><span class="track"><i class={tone} style:left="{(before / segTotal) * 100}%" style:width="{Math.max(0.6, (v / segTotal) * 100)}%"></i></span><span class="mono small r">{v} ms</span></div>
        {/each}
        <div class="tr tot"><span class="small strong">Total</span><span></span><span class="mono small strong r">{t.totalMs} ms</span></div>
        <p class="hint">Measured on the site server (httptrace); the browser ↔ agent time is not included.</p>
      </div>
    {:else if tab === 'redirects'}
      {#if res.redirects.length}
        <ol class="redir">{#each res.redirects as r, i (i)}<li><span class="badge {statusTone(r.status)}">{r.status}</span> <span class="mono">{r.url}</span></li>{/each}</ol>
      {:else}<p class="faint small pad">No redirect.</p>{/if}
    {:else}
      <div class="sent" data-testid="http-sent">
        <div class="mono strong">{res.sent.method} {res.sent.url}</div>
        <table class="table compact kvt"><tbody>{#each res.sent.headers as [k, v], i (i)}<tr><td class="mono k">{k}</td><td class="mono">{v}</td></tr>{/each}</tbody></table>
        <p class="hint">As sent by the agent: placeholders resolved, secrets masked{res.sent.bodyBytes ? ` · body ${bytes(res.sent.bodyBytes)}` : ''}.</p>
      </div>
    {/if}
  </div>
</div>

<style>
  .rv {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
    gap: 6px;
  }
  .sl {
    display: flex;
    gap: 10px;
    align-items: center;
    flex-wrap: wrap;
    min-width: 0;
  }
  .st {
    height: 24px;
    font-size: var(--fs-sm);
    font-weight: var(--fw-semibold);
  }
  .m {
    display: inline-flex;
    gap: 4px;
    align-items: center;
    font-size: var(--fs-sm);
    color: var(--text-2);
  }
  .fu {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 45%;
  }
  .tb {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    overflow: auto;
  }
  .bbar {
    display: flex;
    gap: 8px;
    align-items: center;
    padding: 6px 0;
    flex-wrap: wrap;
  }
  .body {
    flex: 1;
    min-height: 160px;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    overflow: hidden;
    display: flex;
    flex-direction: column;
  }
  .body > :global(*) {
    flex: 1;
  }
  .pre {
    margin: 0;
    overflow: auto;
    padding: 8px 10px;
    font-size: 12px;
    white-space: pre-wrap;
    word-break: break-word;
  }
  .img {
    display: grid;
    place-items: center;
    padding: 16px;
    background: repeating-conic-gradient(var(--surface-2) 0 25%, var(--surface) 0 50%) 0 0 / 16px 16px;
  }
  .img img {
    max-width: 100%;
    max-height: 340px;
    image-rendering: pixelated;
    min-width: 64px;
  }
  .frame {
    border: 0;
    width: 100%;
    min-height: 260px;
    background: #fff;
  }
  .compact :global(td),
  .compact :global(th) {
    padding: 4px 10px;
    font-size: 12.5px;
  }
  .kvt .k {
    color: var(--violet-text);
    white-space: nowrap;
    width: 30%;
  }
  .kvt td {
    word-break: break-all;
  }
  .val {
    max-width: 260px;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .pad {
    padding: 10px;
  }
  .timing {
    padding: 10px 4px;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .tr {
    display: grid;
    grid-template-columns: 120px 1fr 80px;
    gap: 10px;
    align-items: center;
  }
  .track {
    position: relative;
    height: 10px;
    background: var(--gray-soft);
    border-radius: 3px;
  }
  .track i {
    position: absolute;
    top: 0;
    bottom: 0;
    border-radius: 3px;
  }
  .track i.blue {
    background: var(--blue);
  }
  .track i.violet {
    background: var(--violet);
  }
  .track i.amber {
    background: var(--amber);
  }
  .track i.green {
    background: var(--green);
  }
  .track i.accent {
    background: var(--accent);
  }
  .tot {
    border-top: 1px solid var(--border);
    padding-top: 6px;
  }
  .r {
    text-align: right;
  }
  .redir {
    padding-left: 20px;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .sent {
    padding: 8px 2px;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
</style>
