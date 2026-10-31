/**
 * §21 #125 port tunnels on the single-process cloud (local adapter only): add (free port, conflict, bad target), the
 * cloud view lists the tunnel, a client connection is refused while the tunnel is off on its site, then carried over
 * the site's long-polls both ways (client → recv, send → client, close) once the site reports it switched on; a site
 * that does not poll is served through a connected peer that reaches it; remove stops listening.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import net from 'node:net';
import type { PortTunnelNextResponse, PortTunnelStatus, PortTunnelsResponse } from '@opm/shared';
import { BASE, enrollSite, helloDeployment, person, raw, signHeaders, type SiteKey } from './helpers.ts';

async function agentGet<T>(key: SiteKey, dep: string, site: string, path: string): Promise<T> {
  const r = await fetch(BASE + path, { headers: signHeaders(key, dep, site, 'GET', path, null) });
  assert.equal(r.status, 200, await r.clone().text());
  return (await r.json()) as T;
}
async function agentSend(key: SiteKey, dep: string, site: string, connId: string, body: object) {
  const path = `/v1/agent/tunnels/conn/${connId}/send`;
  const b = Buffer.from(JSON.stringify(body));
  const r = await fetch(BASE + path, { method: 'POST', body: b, headers: { ...signHeaders(key, dep, site, 'POST', path, b), 'Content-Type': 'application/json' } });
  assert.equal(r.status, 200, await r.clone().text());
  return (await r.json()) as { accepted: boolean };
}
const nextPath = (q: string) => `/v1/agent/tunnels/next?${q}`;
function connect(port: number): Promise<{ sock: net.Socket; data: () => string; closed: Promise<void> }> {
  return new Promise((resolve, reject) => {
    let buf = '';
    const sock = net.connect(port, '127.0.0.1', () => resolve({ sock, data: () => buf, closed }));
    const closed = new Promise<void>((r) => sock.on('close', () => r()));
    sock.on('data', (d) => (buf += d.toString()));
    sock.on('error', reject);
  });
}
const until = async (f: () => boolean, ms = 5000) => {
  const t = Date.now();
  while (!f()) {
    if (Date.now() - t > ms) throw new Error('timeout');
    await new Promise((r) => setTimeout(r, 20));
  }
};

test('§21 #125 port tunnels: add, refuse while off, relay both ways, via a peer, remove', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const app = await enrollSite(admin, id, 'app');
  const db = await enrollSite(admin, id, 'db');
  const H = { headers: { Authorization: admin.bearer } };

  const l0 = await raw('GET', `/v1/deployments/${id}/tunnels`, H);
  assert.equal(l0.status, 200, l0.body.toString());
  const r0 = l0.json as PortTunnelsResponse;
  assert.equal(r0.available, true, r0.reason ?? '');
  assert.deepEqual(r0.agents.map((a) => a.siteSlug).sort(), ['app', 'db']);
  const port = r0.freePorts[0]!;
  assert.ok(port >= 28700 && port <= 28703);

  // add: bad target / unknown port / then ok / same port again → conflict
  const add = (body: object) => raw('POST', `/v1/deployments/${id}/tunnels`, { headers: { ...H.headers, 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
  assert.equal((await add({ siteSlug: 'app', publicPort: port, target: { kind: 'edge', scheme: 'ftp' } })).status, 422);
  assert.equal((await add({ siteSlug: 'app', publicPort: 1999, target: { kind: 'host', port: 22 } })).status, 422);
  const created = await add({ siteSlug: 'app', publicPort: port, target: { kind: 'host', port: 5432 }, label: 'db' });
  assert.equal(created.status, 201, created.body.toString());
  const t = created.json as PortTunnelStatus;
  assert.equal(t.listening, true, t.listenError ?? '');
  assert.equal(t.enabledOnSite, null);
  assert.equal((await add({ siteSlug: 'db', publicPort: port, target: { kind: 'host', port: 1 } })).status, 409);
  const view = await app.api.poll({ query: { wait: 0 } });
  assert.deepEqual(view.tunnels?.map((x) => [x.tunnelId, x.publicPort]), [[t.tunnelId, port]]);

  // off on the site (it polls without the id) → the client is closed at once
  await agentGet<PortTunnelNextResponse>(app.key, id, 'app', nextPath('wait=0&enabled=&remoteGui=1'));
  const off = await connect(port);
  await off.closed;
  let st = (await raw('GET', `/v1/deployments/${id}/tunnels`, H)).json as PortTunnelsResponse;
  assert.match(st.items[0]!.lastError ?? '', /switched off/);
  assert.equal(st.items[0]!.enabledOnSite, false);
  assert.deepEqual(st.agents.find((a) => a.siteSlug === 'app'), { siteSlug: 'app', connected: true, enabledOnSite: true });

  // switched on: the next poll hands out the connection; bytes both ways; the target closes
  const pollP = agentGet<PortTunnelNextResponse>(app.key, id, 'app', nextPath(`wait=10&enabled=${t.tunnelId}&remoteGui=1`));
  await new Promise((r) => setTimeout(r, 100));
  const cli = await connect(port);
  const opens = (await pollP).opens;
  assert.equal(opens.length, 1);
  assert.deepEqual([opens[0]!.tunnelId, opens[0]!.siteSlug, opens[0]!.target], [t.tunnelId, 'app', { kind: 'host', port: 5432 }]);
  const cid = opens[0]!.connId;
  cli.sock.write('hello target');
  const r1 = await agentGet<{ dataB64: string; closed: boolean }>(app.key, id, 'app', `/v1/agent/tunnels/conn/${cid}/recv?wait=5`);
  assert.equal(Buffer.from(r1.dataB64, 'base64').toString(), 'hello target');
  assert.equal(r1.closed, false);
  assert.deepEqual(await agentGet(db.key, id, 'db', `/v1/agent/tunnels/conn/${cid}/recv?wait=0`), { dataB64: '', closed: true }, 'another site never gets the bytes');
  assert.deepEqual(await agentSend(app.key, id, 'app', cid, { dataB64: Buffer.from('hello client').toString('base64') }), { accepted: true });
  await until(() => cli.data() === 'hello client');
  st = (await raw('GET', `/v1/deployments/${id}/tunnels`, H)).json as PortTunnelsResponse;
  assert.equal(st.items[0]!.openConnections, 1);
  await agentSend(app.key, id, 'app', cid, { dataB64: '', close: true });
  await cli.closed;

  // the client closes first → recv reports closed
  const pollP2 = agentGet<PortTunnelNextResponse>(app.key, id, 'app', nextPath(`wait=10&enabled=${t.tunnelId}&remoteGui=1`));
  await new Promise((r) => setTimeout(r, 100));
  const cli2 = await connect(port);
  const cid2 = (await pollP2).opens[0]!.connId;
  cli2.sock.end('bye');
  let got = '';
  for (let i = 0; i < 5; i++) {
    const r = await agentGet<{ dataB64: string; closed: boolean }>(app.key, id, 'app', `/v1/agent/tunnels/conn/${cid2}/recv?wait=2`);
    got += Buffer.from(r.dataB64, 'base64').toString();
    if (r.closed) break;
  }
  assert.equal(got, 'bye');

  // a tunnel of db (db does not poll) is served through app, which reaches db
  const p2 = (st.freePorts as number[])[0]!;
  const viaT = (await add({ siteSlug: 'db', publicPort: p2, target: { kind: 'container', containerSlug: 'pg', portName: 'db' } })).json as PortTunnelStatus;
  await agentGet<PortTunnelNextResponse>(app.key, id, 'app', nextPath(`wait=0&enabled=${t.tunnelId}&remoteGui=1&reach=db`));
  const viaP = agentGet<PortTunnelNextResponse>(app.key, id, 'app', nextPath(`wait=10&enabled=${t.tunnelId}&remoteGui=1&reach=db`));
  await new Promise((r) => setTimeout(r, 100));
  const cli3 = await connect(p2);
  const o3 = (await viaP).opens[0]!;
  assert.deepEqual([o3.tunnelId, o3.siteSlug], [viaT.tunnelId, 'db']);
  st = (await raw('GET', `/v1/deployments/${id}/tunnels`, H)).json as PortTunnelsResponse;
  assert.deepEqual(st.items.find((x) => x.tunnelId === viaT.tunnelId)!.route, { kind: 'via', siteSlug: 'app' });
  await agentSend(app.key, id, 'app', o3.connId, { dataB64: '', close: true, error: 'db: connection refused' });
  await cli3.closed;

  // remove: no longer listening, the port is free again
  assert.equal((await raw('DELETE', `/v1/deployments/${id}/tunnels/${t.tunnelId}`, H)).status, 200);
  await assert.rejects(connect(port));
  st = (await raw('GET', `/v1/deployments/${id}/tunnels`, H)).json as PortTunnelsResponse;
  assert.ok(st.freePorts.includes(port));
  assert.deepEqual(st.items.map((x) => x.tunnelId), [viaT.tunnelId]);
  assert.equal((await raw('DELETE', `/v1/deployments/${id}/tunnels/${viaT.tunnelId}`, H)).status, 200);
});
