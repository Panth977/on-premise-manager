import { test } from 'node:test';
import assert from 'node:assert/strict';
import { parseTunnelPorts, validatePortTunnel, portTunnelTargetText } from './tunnels.js';

test('TUNNEL_FREE_PORTS: ports and ranges, sorted and unique', () => {
  assert.deepEqual(parseTunnelPorts('2000-2003, 3000,2002 ,2400-2401'), [2000, 2001, 2002, 2003, 2400, 2401, 3000]);
  assert.deepEqual(parseTunnelPorts(''), []);
  assert.deepEqual(parseTunnelPorts(undefined), []);
  assert.throws(() => parseTunnelPorts('80'), /1024-65535/);
  assert.throws(() => parseTunnelPorts('3000-2000'), /1024-65535/);
  assert.throws(() => parseTunnelPorts('abc'), /not a port/);
});

test('validatePortTunnel', () => {
  const ok = { siteSlug: 'app', publicPort: 2001 };
  assert.equal(validatePortTunnel({ ...ok, target: { kind: 'container', containerSlug: 'web', portName: 'http' } }), null);
  assert.equal(validatePortTunnel({ ...ok, target: { kind: 'edge', scheme: 'https' } }), null);
  assert.equal(validatePortTunnel({ ...ok, target: { kind: 'host', port: 5432 } }), null);
  assert.match(validatePortTunnel({ ...ok, target: { kind: 'host', port: 0 } })!, /target.port/);
  assert.match(validatePortTunnel({ ...ok, target: { kind: 'edge', scheme: 'ftp' as 'http' } })!, /scheme/);
  assert.match(validatePortTunnel({ ...ok, publicPort: 80, target: { kind: 'host', port: 1 } })!, /publicPort/);
  assert.match(validatePortTunnel({ siteSlug: 'App!', publicPort: 2001, target: { kind: 'host', port: 1 } })!, /siteSlug/);
  assert.equal(portTunnelTargetText({ kind: 'container', containerSlug: 'pg', portName: 'db' }), 'pg · db');
});
