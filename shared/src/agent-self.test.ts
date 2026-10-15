// SPEC-final §21 #90 — agent self-management: version compare, GUI-upgrade support, settings validation, contract shape.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { CLOUD_API } from './cloud-api.js';
import { AGENT_SELF_API, uninstallCommands, validateAgentSettings, type AgentEditableSettings } from './agent-self.js';
import { ACTIVITY_TYPES, AUDIT_DEPLOYMENT_OPS, AUDIT_OPS, GUI_UPGRADE_MIN_VERSION, agentUpgradeAvailable, compareAgentVersions, supportsGuiUpgrade } from './types.js';

test('compareAgentVersions: core numerically, then the build stamp', () => {
  assert.equal(compareAgentVersions('0.9.0-10080438', '0.10.0-10081200'), -1);
  assert.equal(compareAgentVersions('0.10.0', '0.9.9'), 1);
  assert.equal(compareAgentVersions('0.10.0-10081200', '0.10.0-10081201'), -1);
  assert.equal(compareAgentVersions('0.10.0-10081200', '0.10.0-10081200'), 0);
  assert.equal(compareAgentVersions('0.10.0', '0.10.0-10081200'), -1);
  assert.equal(compareAgentVersions('1.0.0-dev', '1.0.0-10081200'), 1); // string order when not both numeric
  assert.equal(compareAgentVersions('garbage', '0.1.0'), -1);
  assert.equal(compareAgentVersions('v0.7.2-10080232', '0.7.2-10080232'), 0);
});

test('supportsGuiUpgrade + agentUpgradeAvailable', () => {
  assert.equal(GUI_UPGRADE_MIN_VERSION, '0.10.0');
  assert.equal(supportsGuiUpgrade('0.7.2-10080232'), false);
  assert.equal(supportsGuiUpgrade('0.9.0-10080438'), false);
  assert.equal(supportsGuiUpgrade('0.10.0-10081200'), true);
  assert.equal(supportsGuiUpgrade('1.2.3'), true);
  assert.equal(supportsGuiUpgrade(null), false);
  assert.equal(agentUpgradeAvailable('0.10.0-10081200', '0.10.1-10090000'), true);
  assert.equal(agentUpgradeAvailable('0.10.1-10090000', '0.10.1-10090000'), false);
  assert.equal(agentUpgradeAvailable('0.10.1', null), false);
});

const ok: AgentEditableSettings = { tools: { hostShell: true, containerExec: true }, containerNames: 'plain', edgeHttpPort: 80, guiAliasPort: 8080 };

test('validateAgentSettings', () => {
  assert.deepEqual(validateAgentSettings(ok, { agentPort: 7080 }), []);
  const codes = (s: AgentEditableSettings) => validateAgentSettings(s, { agentPort: 7080, previous: ok }).map((i) => `${i.path}:${i.code}:${i.severity}`);
  assert.deepEqual(codes({ ...ok, edgeHttpPort: 7080 }), ['edgeHttpPort:V_PORT_CLASH:error']);
  assert.deepEqual(codes({ ...ok, guiAliasPort: 80 }), ['guiAliasPort:V_PORT_CLASH:error']);
  assert.deepEqual(codes({ ...ok, guiAliasPort: 70000 }), ['guiAliasPort:V_PORT:error']);
  assert.deepEqual(codes({ ...ok, guiAliasPort: null }), []);
  assert.deepEqual(codes({ ...ok, edgeHttpPort: null }), ['edgeHttpPort:V_PORT:warning']);
  assert.deepEqual(codes({ ...ok, containerNames: 'prefixed' }), ['containerNames:V_CONTAINER_NAMES:info']);
  assert.deepEqual(codes({ ...ok, containerNames: 'x' as 'plain' }), ['containerNames:V_ENUM:error']);
});

test('agent self API: writes need the actor; upgrade lists its activities', () => {
  for (const [name, d] of Object.entries(AGENT_SELF_API)) {
    if (d.method !== 'GET' && name !== 'checkRelease') assert.equal(d.auth, 'session+actor', name);
  }
  for (const t of ['agent.upgrade-started', 'agent.upgraded', 'agent.upgrade-failed', 'agent.rolled-back', 'agent.restarted', 'agent.config-changed', 'agent.support-bundle-downloaded', 'agent.upgrade-requested', 'agent.upgrade-cancelled'])
    assert.ok((ACTIVITY_TYPES as readonly string[]).includes(t), t);
  assert.ok(AGENT_SELF_API.startUpgrade.errors.includes('locked'));
});

test('cloud upgrade ops are audited deployment ops', () => {
  for (const op of ['requestAgentUpgrade', 'cancelAgentUpgrade', 'upgradeAllAgents', 'cancelAgentUpgradeRun'] as const) {
    assert.ok((CLOUD_API as Record<string, unknown>)[op], op);
    assert.ok((AUDIT_DEPLOYMENT_OPS as readonly string[]).includes(op), op);
    assert.ok(op in AUDIT_OPS, op);
  }
});

test('uninstallCommands', () => {
  const l = uninstallCommands({ configPath: '/etc/opm/agent.json', dataDir: '/var/lib/opm', binaryPath: '/usr/local/bin/opm-agent', systemd: true });
  assert.ok(l[0].includes('opm-agent.socket'));
  assert.ok(l.some((x) => x.includes('/usr/local/bin/opm-agent.prev')));
  assert.ok(l.at(-1)!.includes('/var/lib/opm'));
});

test('§21 #91 install action + schema compatibility', async () => {
  const { agentInstallAction, agentSchemaCompatible, AGENT_LEGACY_SCHEMA } = await import('./types.js');
  assert.equal(agentInstallAction(null, '0.10.0-1'), 'fresh');
  assert.equal(agentInstallAction('0.7.2-10080232', '0.10.0-10081200'), 'upgrade');
  assert.equal(agentInstallAction('0.10.1-10090000', '0.10.0-10081200'), 'downgrade');
  assert.equal(agentInstallAction('0.10.0-10081200', '0.10.0-10081200'), 'same');
  assert.equal(AGENT_LEGACY_SCHEMA, 1);
  assert.equal(agentSchemaCompatible(1, {}), true); //               legacy target opens a schema-1 DB
  assert.equal(agentSchemaCompatible(2, {}), false); //              legacy target cannot open schema 2
  assert.equal(agentSchemaCompatible(2, { schema: 3, minSchema: 1 }), true);
  assert.equal(agentSchemaCompatible(null, { schema: 2, minSchema: 0 }), true);
  assert.equal(agentSchemaCompatible(1, { schema: 3, minSchema: 2 }), false);
});
