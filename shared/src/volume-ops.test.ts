// SPEC-final §21 #96 volume manager — shared helpers + contract.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { AGENT_API } from './agent-api.js';
import { TOOLS_API } from './agent-tools.js';
import { ACTIVITY_TYPES } from './types.js';
import { copyPathProblem, dataDirMismatch, defaultCopyPath, isDatabaseImage, volumeConfirmOk } from './volume-ops.js';

test('database images (stop during copy by default)', () => {
  for (const i of ['postgres:16', 'docker.io/library/postgres:17-alpine', 'northwind/custom-pg:17', 'timescale/timescaledb:2', 'mysql:8', 'mariadb', 'mongo:7', 'redis:7', 'influxdb:2.7', 'clickhouse/clickhouse-server', 'eclipse-mosquitto:2'])
    assert.equal(isDatabaseImage(i), true, i);
  for (const i of ['nginx:1.27', 'node:22', 'northwind/northwind-backend', 'grafana/grafana', '', null]) assert.equal(isDatabaseImage(i), false, String(i));
});

test('data-dir mismatch banners', () => {
  assert.equal(dataDirMismatch('\x1b[31m*** DATA DIRECTORY / IMAGE VERSION MISMATCH ***\x1b[0m\nmore')?.kind, 'custom-pg');
  const pg = dataDirMismatch('2026-10-08 FATAL:  database files are incompatible with server\nDETAIL: The data directory was initialized by PostgreSQL version 16');
  assert.equal(pg?.kind, 'postgres');
  assert.match(pg!.line, /incompatible/);
  assert.equal(dataDirMismatch('LOG: database system is ready to accept connections'), null);
});

test('copy paths', () => {
  assert.equal(defaultCopyPath('/var/lib/opm/', 'pg-data', new Date(Date.UTC(2026, 9, 8, 7, 5, 9))), '/var/lib/opm/volume-copies/pg-data-20261008-070509');
  const home = '/var/lib/opm';
  assert.equal(copyPathProblem('', home), null);
  assert.equal(copyPathProblem('/srv/backup/pg', home), null);
  assert.equal(copyPathProblem('./volume-copies/x', home), null);
  for (const bad of ['/', '/etc/x', '/usr', '/var/lib/docker/volumes/x', 'relative/x', '/var/lib/opm', '/var/lib/opm/volumes/x', './vol/x', '/srv/../etc/passwd', '/var/lib/opm/volume-copies'])
    assert.notEqual(copyPathProblem(bad, home), null, bad);
});

test('confirm = slug; contract', () => {
  assert.equal(volumeConfirmOk('pg-data', ' pg-data '), true);
  assert.equal(volumeConfirmOk('pg-data', 'PG-DATA'), false);
  assert.equal(AGENT_API.emptyVolume.ok, 202);
  assert.equal(AGENT_API.copyVolume.auth, 'session+actor');
  assert.equal(TOOLS_API.uploadVolumeFile.body, 'raw');
  for (const t of ['volume.emptied', 'volume.copied', 'volume.restored', 'volume.file-written', 'volume.file-deleted']) assert.ok((ACTIVITY_TYPES as readonly string[]).includes(t), t);
});
