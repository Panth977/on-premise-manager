/** Masking + view conversions (secrets and registry passwords are never in normal reads, §3). */
import { MASK, type Account, type AccountDoc, type AgentDoc, type Config, type ConfigView, type DeploymentView, type Registry, type RegistryView, type Revision, type RevisionView, type SecretDef, type SecretDefView, type SrcTemplate, type SrcTemplateView } from '@opm/shared';
import type { StoredDeployment } from './codec.js';
import { pub } from './fire.js';

export const isSecretSet = (s: Pick<SecretDef, 'value'>) => s.value !== null && s.value !== undefined && s.value !== '';
export function maskSecret(s: SecretDef): SecretDefView {
  const set = isSecretSet(s);
  return { ...s, value: set ? MASK : null, isSet: set };
}
export function maskConfig(c: Config): ConfigView {
  return { ...c, secrets: (c.secrets ?? []).map(maskSecret) };
}
export function registryView(r: Registry): RegistryView {
  const p = pub<Registry>(r as unknown as Record<string, unknown>);
  return { ...p, password: MASK, isSet: !!r.password };
}
export function deploymentView(d: StoredDeployment): DeploymentView {
  // §21 #84: the access key plaintext (and its hash) never leave through a view — only create / rotate / revealAccessKey
  const { draft, srcTemplate, accessKeyHash, accessKey, _cloudLastHash, ...rest } = d;
  void accessKeyHash;
  void accessKey;
  void _cloudLastHash;
  return {
    ...pub<Omit<DeploymentView, 'draft' | 'srcTemplate'>>(rest as unknown as Record<string, unknown>),
    draft: maskConfig(draft),
    srcTemplate: srcTemplateView(srcTemplate),
    // §21 #105: a 0.14.0 `branch.uncommitted` (until migrate-j) is never shown
    ...(rest.branch ? { branch: (({ uncommitted: _u, ...b }) => (void _u, b))(rest.branch as typeof rest.branch & { uncommitted?: number }) } : {}),
  };
}
/** §21 #94 the public part of Deployment.srcTemplate (no snapshots). */
export function srcTemplateView(s: SrcTemplate | null): SrcTemplateView | null {
  if (!s) return null;
  return {
    templateId: s.templateId,
    templateVersion: s.templateVersion,
    copiedAt: s.copiedAt,
    label: s.snapshot?.label ?? '',
    baseVersion: s.baseVersion,
    lastSyncedAt: s.lastSyncedAt,
    lastSyncedBy: s.lastSyncedBy,
    skippedCount: s.skipped.length,
  };
}
export function revisionView(r: Revision): RevisionView {
  return { ...r, config: maskConfig(r.config) };
}
export const ONLINE_MS = 90_000;
export function agentView(a: AgentDoc): AgentDoc {
  const p = pub<AgentDoc>(a as unknown as Record<string, unknown>);
  return { ...p, detectedAddresses: Array.isArray(p.detectedAddresses) ? p.detectedAddresses : [], online: Date.now() - Date.parse(a.lastSeenAt) < ONLINE_MS };
}

/**
 * accounts/{uid} (AccountDoc, with stored credential values) → API Account (§21 #84: metadata only — the values come
 * only from the reveal endpoints). Docs written before round G lack the fields → null.
 */
export function accountView(d: AccountDoc | Record<string, unknown>): Account {
  const doc = pub<AccountDoc>(d as Record<string, unknown>);
  const pw = doc.lastSetPassword ?? null;
  const link = doc.lastResetLink ?? null;
  return {
    ...doc,
    lastSetPassword: pw ? { setAt: pw.setAt, setBy: pw.setBy } : null,
    lastResetLink: link ? { setAt: link.setAt, setBy: link.setBy, expiresAt: link.expiresAt } : null,
  };
}
