/** Root audit log helpers for the console (SPEC-final §6.2): defensive entry normalisation, target links, labels. */
import { AUDIT_ACTIONS, isAuditTruncated, type ActivityType, type AuditAction, type Account, type AuditChange, type AuditLogEntry, type AuditTargetType } from '@opm/shared';

/** Entries written before the audit change lack the new fields: fill safe defaults. */
export function normalizeAudit(e: Partial<AuditLogEntry> & { auditId: string; ts: string }): AuditLogEntry {
  return {
    auditId: e.auditId,
    ts: e.ts,
    op: e.op ?? '',
    actor: e.actor ?? 'system',
    actorEmail: e.actorEmail ?? null,
    action: e.action ?? '',
    outcome: e.outcome === 'error' ? 'error' : 'ok',
    status: typeof e.status === 'number' ? e.status : 200,
    errorCode: e.errorCode ?? null,
    errorMessage: e.errorMessage ?? null,
    targetType: (e.targetType ?? targetTypeOf(e.target ?? '')) as AuditTargetType,
    target: e.target ?? '',
    requestId: e.requestId ?? null,
    ip: e.ip ?? null,
    userAgent: e.userAgent ?? null,
    changes: Array.isArray(e.changes) ? (e.changes as AuditChange[]) : [],
    changesTotal: typeof e.changesTotal === 'number' ? e.changesTotal : Array.isArray(e.changes) ? e.changes.length : 0,
    data: e.data && typeof e.data === 'object' ? e.data : {},
  };
}

function targetTypeOf(target: string): AuditTargetType {
  const p = target.split('/');
  if (p[0] === 'services' && p[2] === 'images') return 'image';
  const m: Record<string, AuditTargetType> = { registries: 'registry', services: 'service', notFoundPages: 'notFoundPage', templates: 'template', tags: 'tag', accounts: 'account', deployments: 'deployment', uploads: 'upload', blobs: 'blob', icons: 'icon', githubMirror: 'github' };
  return m[p[0] ?? ''] ?? 'system';
}

/** Console page of an audit target, or null when it has none. */
export function auditTargetHref(target: string): string | null {
  const p = target.split('/');
  const [kind, id] = p;
  if (!id) return null;
  const e = encodeURIComponent;
  switch (kind) {
    case 'registries':
      return `/app/registries?registry=${e(id)}`;
    case 'services':
      if (p[2] === 'images' && p[3]) return `/app/services/${e(id)}/image/${e(p[3])}`;
      if (p[2]) return null;
      return `/app/services/${e(id)}`;
    case 'notFoundPages':
      return `/app/not-found-pages/${e(id)}`;
    case 'templates':
      return `/app/templates/${e(id)}`;
    case 'tags':
      return `/app/tags?tag=${e(id)}`;
    case 'accounts':
      return `/app/accounts/${e(id)}`;
    case 'deployments':
      return `/app/deployments/${e(id)}`;
    case 'githubMirror':
      return '/app/github';
    default:
      return null;
  }
}

/** actorEmail, else the account name from the catalog, else "system" / the uid */
export function auditActorLabel(e: Pick<AuditLogEntry, 'actor' | 'actorEmail'>, accounts: Account[]): string {
  if (e.actor === 'system') return 'system';
  if (e.actorEmail) return e.actorEmail;
  return accounts.find((a) => a.uid === e.actor)?.name ?? e.actor;
}

export function auditActionTone(action: string): string {
  if (/revealed|password-reset|password-set|reset-link|rotated|regenerated/.test(action)) return 'amber';
  if (/deleted|disabled|failed/.test(action)) return 'red';
  if (/agent-upgrade/.test(action)) return 'violet';
  if (/access-changed|enabled/.test(action)) return 'violet';
  if (/created|registered|ready|uploaded|completed/.test(action)) return 'green';
  return 'blue';
}

/** "template." … prefixes of every catalog action (filter options) */
export const AUDIT_ACTION_PREFIXES: string[] = [...new Set(AUDIT_ACTIONS.map((a) => a.split('.')[0] + '.'))];

export const AUDIT_TARGET_TYPE_LABELS: Record<AuditTargetType, string> = {
  registry: 'Registry',
  service: 'Service',
  image: 'Image',
  notFoundPage: '404 page',
  template: 'Template',
  tag: 'Tag',
  account: 'Account',
  deployment: 'Deployment',
  upload: 'Upload',
  icon: 'Icon',
  blob: 'Blob',
  system: 'System',
  github: 'GitHub mirror',
};

/** A value of a change for display: strings as-is, objects pretty JSON; truncated values as their preview. */
export function auditValueText(v: unknown): { text: string; truncated: number | null; empty: boolean } {
  if (isAuditTruncated(v)) return { text: v.preview, truncated: v.bytes, empty: false };
  if (v === null || v === undefined) return { text: '—', truncated: null, empty: true };
  if (typeof v === 'string') return { text: v === '' ? '""' : v, truncated: null, empty: false };
  if (typeof v === 'object') return { text: JSON.stringify(v, null, 2), truncated: null, empty: false };
  return { text: String(v), truncated: null, empty: false };
}

/** Human labels of audit actions (credential actions of SPEC-final §21 #84 spelled out); others are derived. */
const AUDIT_ACTION_LABELS: Partial<Record<AuditAction, string>> = {
  'registry.password-revealed': 'Registry password viewed',
  'account.password-reset': 'Password-set link made (legacy)',
  'account.password-set': 'New password set',
  'account.password-revealed': 'Password viewed',
  'account.reset-link-created': 'Reset link made',
  'account.reset-link-revealed': 'Reset link viewed',
  'deployment.access-key-revealed': 'Access key viewed',
  'deployment.access-key-rotated': 'Access key regenerated',
  'deployment.install-command-created': 'Install command created',
  'deployment.install-command-revealed': 'Install command viewed',
  'deployment.secret-revealed': 'Secret viewed',
  'deployment.secret-regenerated': 'Secret regenerated',
  'deployment.agent-upgrade-requested': 'Agent upgrade requested',
  'deployment.agent-upgrade-cancelled': 'Agent upgrade cancelled',
};
/** Human labels of activity types (cloud credential activities of §21 #84 spelled out); others are derived. */
const ACTIVITY_TYPE_LABELS: Partial<Record<ActivityType, string>> = {
  'access-key.revealed': 'Access key viewed',
  'access-key.rotated': 'Access key regenerated',
  'install-token.created': 'Install command created',
  'install-token.revoked': 'Install command revoked',
  'install-token.revealed': 'Install command viewed',
  'secret.revealed': 'Secret viewed',
  'secret.regenerated': 'Secret regenerated',
  // §21 #90 agent self-management
  'agent.upgrade-started': 'Agent upgrade started',
  'agent.upgraded': 'Agent upgraded',
  'agent.upgrade-failed': 'Agent upgrade failed',
  'agent.rolled-back': 'Agent rolled back',
  'agent.restarted': 'Agent restarted',
  'agent.config-changed': 'Agent settings changed',
  'agent.support-bundle-downloaded': 'Support bundle downloaded',
  'agent.upgrade-requested': 'Agent upgrade requested',
  'agent.upgrade-cancelled': 'Agent upgrade cancelled',
};
function derivedLabel(code: string): string {
  const [head, ...rest] = code.split('.');
  const t = `${(head ?? '').replace(/-/g, ' ')} ${rest.join(' ').replace(/-/g, ' ')}`.trim();
  return t.charAt(0).toUpperCase() + t.slice(1);
}
export function auditActionLabel(action: string): string {
  return AUDIT_ACTION_LABELS[action as AuditAction] ?? derivedLabel(action);
}
export function activityTypeLabel(type: string): string {
  return ACTIVITY_TYPE_LABELS[type as ActivityType] ?? derivedLabel(type);
}
