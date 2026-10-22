/**
 * §21 #118 remote agent GUI on the single-process cloud (local adapter only): no cookie → 401 page; openRemoteGui sets
 * the cookie (Path = the remote prefix) and lists the site in the cloud view; a browser request waits until the site's
 * long-poll takes it (only that site's), the posted answer comes back with every /_opm path re-prefixed, redirects
 * rewritten, the agent's cookies dropped; a WebSocket is relayed over the site's long-polls; an event stream without follow is one answer, with follow 501.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { remoteGuiPrefix, type RemoteGuiNextResponse } from '@opm/shared';
import { ADAPTER, BASE, enrollSite, helloDeployment, person, raw, signHeaders, type SiteKey } from './helpers.ts';

async function next(key: SiteKey, dep: string, site: string, wait: number): Promise<RemoteGuiNextResponse> {
  const path = `/v1/agent/remote-gui/next?wait=${wait}`;
  const r = await fetch(BASE + path, { headers: signHeaders(key, dep, site, 'GET', path, null) });
  assert.equal(r.status, 200, await r.clone().text());
  return (await r.json()) as RemoteGuiNextResponse;
}
async function answer(key: SiteKey, dep: string, site: string, reqId: string, status: number, headers: Record<string, string>, body: string) {
  const path = `/v1/agent/remote-gui/${reqId}/result`;
  const b = Buffer.from(body);
  const r = await fetch(BASE + path, {
    method: 'POST',
    body: b,
    headers: { ...signHeaders(key, dep, site, 'POST', path, b), 'Content-Type': 'application/octet-stream', 'X-OPM-Remote-Status': String(status), 'X-OPM-Remote-Headers': Buffer.from(JSON.stringify(headers)).toString('base64') },
  });
  return (await r.json()) as { accepted: boolean };
}

test('§21 #118 remote agent GUI: session cookie, long-poll hand-off, prefix rewriting, WebSocket relay, SSE (follow 501)', { skip: ADAPTER !== 'local' && 'needs the single-process cloud (OPM_TEST_ADAPTER=local)' }, async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const app = await enrollSite(admin, id, 'app');
  const db = await enrollSite(admin, id, 'db');
  const prefix = remoteGuiPrefix(id, 'app');

  // no remote session → 401 page
  const anon = await raw('GET', `${prefix}/_opm/app/app/status`);
  assert.equal(anon.status, 401);
  assert.match(anon.body.toString(), /Open agent GUI/);

  // open: cookie with the remote prefix as Path; the cloud view lists the site
  const o = await raw('POST', `/v1/deployments/${id}/remote-gui/app/session`, { headers: { Authorization: admin.bearer } });
  assert.equal(o.status, 200, o.body.toString());
  assert.equal(o.json.url, `${prefix}/_opm/app/app/status`);
  const setCookie = o.headers.get('set-cookie') ?? '';
  assert.match(setCookie, new RegExp(`^opm_remote_app=[^;]+; Path=${prefix.replace(/\//g, '\\/')};`));
  assert.match(setCookie, /HttpOnly/);
  const cookie = setCookie.split(';')[0]!;
  const view = await app.api.poll({ query: { wait: 0 } });
  assert.deepEqual(view.remoteGui?.map((x) => x.siteSlug), ['app']);
  await assert.rejects(raw('POST', `/v1/deployments/${id}/remote-gui/nope/session`, { headers: { Authorization: admin.bearer } }).then((r) => (r.status === 404 ? Promise.reject(new Error('404')) : r)));

  // the bare prefix → the site's status page
  const bare = await raw('GET', prefix, { headers: { Cookie: cookie } });
  assert.equal(bare.status, 302);
  assert.equal(bare.headers.get('location'), `${prefix}/_opm/app/app/status`);

  // a page: waits for the site's long-poll, comes back rewritten
  const pageP = raw('GET', `${prefix}/_opm/app/app/status?x=1`, { headers: { Cookie: cookie, Accept: 'text/html' } });
  assert.deepEqual((await next(db.key, id, 'db', 0)).items, [], 'another site never sees the request');
  const got = await next(app.key, id, 'app', 10);
  assert.equal(got.items.length, 1);
  const it = got.items[0]!;
  assert.equal(it.method, 'GET');
  assert.equal(it.path, '/_opm/app/app/status?x=1');
  assert.equal(it.actor.email, admin.email);
  assert.equal(it.headers.accept, 'text/html');
  assert.equal(it.headers.cookie, undefined, 'the cloud cookie never reaches the site');
  const html = `<html><head><link href="/_opm/assets/app.css"><script>const base = "/_opm"; fetch('/_opm/api/session'); const re = /\\/_opm\\//;</script></head><body><a href="/_opm/app">x</a></body></html>`;
  assert.deepEqual(await answer(app.key, id, 'app', it.reqId, 200, { 'Content-Type': 'text/html; charset=utf-8', 'Set-Cookie': 'opm_session_app=s; Path=/_opm', ETag: '"1"' }, html), { accepted: true });
  const pg = await pageP;
  assert.equal(pg.status, 200);
  const text = pg.body.toString();
  assert.ok(text.includes(`href="${prefix}/_opm/assets/app.css"`), text);
  assert.ok(text.includes(`const base = "${prefix}/_opm"`), text);
  assert.ok(text.includes(`fetch('${prefix}/_opm/api/session')`), text);
  assert.ok(text.includes(`href="${prefix}/_opm/app"`), text);
  assert.ok(text.includes('/\\/_opm\\//'), 'escaped forms stay');
  assert.equal(pg.headers.get('set-cookie'), null, "the agent's cookies never reach the browser");
  assert.equal(pg.headers.get('etag'), null);
  // a second answer to the same request is not accepted
  assert.deepEqual(await answer(app.key, id, 'app', it.reqId, 200, {}, 'again'), { accepted: false });

  // a POST with a body + a redirect answer (Location re-prefixed); JSON is passed through unchanged
  const postP = raw('POST', `${prefix}/_opm/api/actor`, { headers: { Cookie: cookie, 'Content-Type': 'application/json' }, body: '{"email":"x@y.z"}' });
  const p2 = (await next(app.key, id, 'app', 10)).items[0]!;
  assert.equal(p2.method, 'POST');
  assert.equal(Buffer.from(p2.bodyB64!, 'base64').toString(), '{"email":"x@y.z"}');
  await answer(app.key, id, 'app', p2.reqId, 302, { Location: '/_opm/app/app/branch', 'Content-Type': 'application/json' }, '{"href":"/_opm/x"}');
  const post = await postP;
  assert.equal(post.status, 302);
  assert.equal(post.headers.get('location'), `${prefix}/_opm/app/app/branch`);
  assert.equal(post.body.toString(), '{"href":"/_opm/x"}');

  // a WebSocket (§21 #118 g): the browser upgrades at the cloud; the site answers 101 and relays the frames over its
  // HTTP long-polls (recv / send) — the site never opens a socket to the cloud
  const ws = new WebSocket(`${BASE.replace(/^http/, 'ws')}${prefix}/_opm/api/sites/app/tools/terminal`, { headers: { Cookie: cookie } } as unknown as string[]);
  ws.binaryType = 'arraybuffer';
  const opened = new Promise<void>((res, rej) => ((ws.onopen = () => res()), (ws.onerror = () => rej(new Error('ws error')))));
  const wsItem = (await next(app.key, id, 'app', 10)).items[0]!;
  assert.ok(wsItem.ws?.streamId, 'the request names its stream');
  assert.equal(wsItem.path, '/_opm/api/sites/app/tools/terminal');
  assert.deepEqual(await answer(app.key, id, 'app', wsItem.reqId, 101, {}, ''), { accepted: true });
  await opened;
  const sp = `/v1/agent/remote-gui/ws/${wsItem.ws!.streamId}`;
  const agentCall = async (method: 'GET' | 'POST', path: string, body: unknown) => {
    const b = body === null ? null : Buffer.from(JSON.stringify(body));
    const r = await fetch(BASE + path, { method, ...(b ? { body: b } : {}), headers: { ...signHeaders(app.key, id, 'app', method, path, b), ...(b ? { 'Content-Type': 'application/json' } : {}) } });
    assert.equal(r.status, 200, await r.clone().text());
    return r.json();
  };
  const gotMsg = new Promise<string>((res) => (ws.onmessage = (e) => res(typeof e.data === 'string' ? e.data : Buffer.from(e.data as ArrayBuffer).toString())));
  assert.deepEqual(await agentCall('POST', `${sp}/send`, { frames: [{ op: 'text', dataB64: Buffer.from('$ hello').toString('base64') }] }), { accepted: true });
  assert.equal(await gotMsg, '$ hello');
  ws.send('ls\n');
  const rv = (await agentCall('GET', `${sp}/recv?wait=5`, null)) as { frames: { op: string; dataB64: string }[]; closed: boolean };
  assert.deepEqual(rv.frames.map((f) => [f.op, Buffer.from(f.dataB64, 'base64').toString()]), [['text', 'ls\n']]);
  const closedP = new Promise<void>((res) => (ws.onclose = () => res()));
  ws.close();
  await closedP;
  const rc = (await agentCall('GET', `${sp}/recv?wait=5`, null)) as { closed: boolean };
  assert.equal(rc.closed, true, 'the browser closing ends the stream for the site');
  // an event stream is not carried (v1)
  const sse = await raw('GET', `${prefix}/_opm/api/sites/app/tools/logs/containers?follow=true`, { headers: { Cookie: cookie, Accept: 'text/event-stream' } });
  assert.equal(sse.status, 501);
  // without follow the stream ends by itself: carried as one answer
  const logsP = raw('GET', `${prefix}/_opm/api/sites/app/tools/logs/containers?tail=10&follow=false`, { headers: { Cookie: cookie, Accept: 'text/event-stream' } });
  const lg = (await next(app.key, id, 'app', 10)).items[0]!;
  assert.equal(lg.path, '/_opm/api/sites/app/tools/logs/containers?tail=10&follow=false');
  const sseBody = 'event: line\ndata: {"t":"x"}\n\nevent: end\ndata: {"reason":"done"}\n\n';
  await answer(app.key, id, 'app', lg.reqId, 200, { 'Content-Type': 'text/event-stream' }, sseBody);
  const logs = await logsP;
  assert.equal(logs.status, 200);
  assert.equal(logs.body.toString(), sseBody);
  // the cookie of site app does not open site db
  const other = await raw('GET', `${remoteGuiPrefix(id, 'db')}/_opm/app/db/status`, { headers: { Cookie: cookie.replace('opm_remote_app', 'opm_remote_db') } });
  assert.equal(other.status, 401);
});
