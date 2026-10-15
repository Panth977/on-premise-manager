/**
 * Cloud access-rule evaluation. Normative: SPEC-final.md §5. Used by the backend (enforcement) and the
 * frontend (hide actions). Deny by default.
 */
import type { Account, AccessRule, Action, DeveloperResource, Resource, Scope, ValidationIssue } from './types.js';

export const DEVELOPER_RESOURCES: readonly DeveloperResource[] = ['registries', 'services', 'images', 'notFoundPages', 'templates', 'tags', 'accounts'];
export const RESOURCES: readonly Resource[] = [
  ...DEVELOPER_RESOURCES,
  'deployments',
  'deployment.setup',
  'deployment.config',
  'deployment.secrets',
  'deployment.activities',
  'deployment.transfers',
];
export const VALID_ACTIONS: Record<Resource, readonly Action[]> = {
  registries: ['read', 'write'],
  services: ['read', 'write'],
  images: ['read', 'write'],
  notFoundPages: ['read', 'write'],
  templates: ['read', 'write'],
  tags: ['read', 'write'],
  accounts: ['read', 'write', 'admin'],
  deployments: ['read', 'write'],
  'deployment.setup': ['read', 'write'],
  'deployment.config': ['read', 'write', 'publish'],
  'deployment.secrets': ['read', 'write'],
  'deployment.activities': ['read', 'write'],
  'deployment.transfers': ['read', 'write'],
};
/** Catalog resources every enabled account may READ (needed to edit any deployment). */
export const IMPLICIT_READ: readonly Resource[] = ['services', 'images', 'notFoundPages', 'tags'];

export interface AccessTarget {
  deploymentId: string;
  tagSlugs: string[];
  managerAccountUids: string[];
}

export interface AccessDecision {
  allowed: boolean;
  reason: string;
  ruleIndex: number | null;
}

export function isDeploymentResource(r: Resource): boolean {
  return r === 'deployments' || r.startsWith('deployment.');
}

function scopeContains(scope: Scope, t: AccessTarget | null): boolean {
  if (scope.kind === 'all') return true;
  if (!t) return false;
  if (scope.kind === 'tags') return scope.tagSlugs.some((s) => t.tagSlugs.includes(s));
  return scope.deploymentIds.includes(t.deploymentId);
}

/**
 * Evaluate one permission.
 * Order: disabled → deny; implicit catalog read → allow; manager of the target (deployment.* any action,
 * deployments read) → allow; first rule with the resource + action + a scope that contains the target → allow;
 * else deny. Developer resources only match rules with scope all. A deployment resource without a target
 * (e.g. "can this person create deployments at all") matches only scope all.
 */
export function can(account: Pick<Account, 'uid' | 'access' | 'disabled'> | null, resource: Resource, action: Action, target: AccessTarget | null = null): AccessDecision {
  if (!account) return { allowed: false, reason: 'not logged in', ruleIndex: null };
  if (account.disabled) return { allowed: false, reason: 'account disabled', ruleIndex: null };
  if (action === 'read' && IMPLICIT_READ.includes(resource)) return { allowed: true, reason: 'catalog read', ruleIndex: null };
  if (target && target.managerAccountUids.includes(account.uid)) {
    if (resource.startsWith('deployment.')) return { allowed: true, reason: 'manager of the deployment', ruleIndex: null };
    if (resource === 'deployments' && action === 'read') return { allowed: true, reason: 'manager of the deployment', ruleIndex: null };
  }
  const dev = (DEVELOPER_RESOURCES as readonly string[]).includes(resource);
  for (let i = 0; i < account.access.length; i++) {
    const r = account.access[i]!;
    if (r.resource !== resource || !r.actions.includes(action)) continue;
    if (dev) {
      if (r.scope.kind === 'all') return { allowed: true, reason: `rule #${i + 1} (scope all)`, ruleIndex: i };
      continue;
    }
    if (scopeContains(r.scope, target)) return { allowed: true, reason: `rule #${i + 1} (${r.scope.kind})`, ruleIndex: i };
  }
  return { allowed: false, reason: `no rule gives ${resource}.${action}${target ? ` on ${target.deploymentId}` : ''}`, ruleIndex: null };
}

export function allowed(account: Pick<Account, 'uid' | 'access' | 'disabled'> | null, resource: Resource, action: Action, target: AccessTarget | null = null): boolean {
  return can(account, resource, action, target).allowed;
}

/** Can the account see the deployment at all (list / open the deployment scope)? */
export function canSeeDeployment(account: Pick<Account, 'uid' | 'access' | 'disabled'> | null, t: AccessTarget): boolean {
  if (allowed(account, 'deployments', 'read', t)) return true;
  // any deployment.* read on this deployment also lets the person open it
  return (['deployment.setup', 'deployment.config', 'deployment.secrets', 'deployment.activities', 'deployment.transfers'] as const).some((r) => allowed(account, r, 'read', t));
}

/** "Admin" = has accounts.admin with scope all. */
export function isAdmin(account: Pick<Account, 'uid' | 'access' | 'disabled'> | null): boolean {
  return allowed(account, 'accounts', 'admin', null);
}

/** The rule set the bootstrap script gives the first admin: every resource, every valid action, scope all. */
export function fullAdminRules(): AccessRule[] {
  return RESOURCES.map((resource) => ({ resource, actions: [...VALID_ACTIONS[resource]], scope: { kind: 'all' as const } }));
}

/** Validate access rules (POST/PATCH accounts). existingTags / existingDeployments: ids that exist. */
export function validateAccessRules(rules: unknown, existingTags?: readonly string[], existingDeployments?: readonly string[]): ValidationIssue[] {
  const out: ValidationIssue[] = [];
  const e = (path: string, code: string, message: string) => out.push({ path, code, message, severity: 'error' });
  if (!Array.isArray(rules)) {
    e('access', 'V_TYPE', 'access must be a list');
    return out;
  }
  if (rules.length > 100) e('access', 'V_TOO_MANY', 'at most 100 rules');
  rules.forEach((r: Partial<AccessRule>, i) => {
    const p = `access[${i}]`;
    if (!r || typeof r !== 'object') return e(p, 'V_TYPE', 'a rule must be an object');
    if (!RESOURCES.includes(r.resource as Resource)) return e(`${p}.resource`, 'V_ENUM', `unknown resource "${String(r.resource)}"`);
    const res = r.resource as Resource;
    if (!Array.isArray(r.actions) || r.actions.length < 1 || r.actions.length > 4) e(`${p}.actions`, 'V_LENGTH', 'actions must have 1–4 items');
    else {
      if (new Set(r.actions).size !== r.actions.length) e(`${p}.actions`, 'V_DUPLICATE', 'actions must be unique');
      for (const a of r.actions) if (!VALID_ACTIONS[res].includes(a)) e(`${p}.actions`, 'V_ACCESS_ACTION', `"${a}" is not valid on ${res} (use ${VALID_ACTIONS[res].join(', ')})`);
    }
    const s = r.scope as Scope | undefined;
    if (!s || !['all', 'tags', 'deployments'].includes(s.kind)) return e(`${p}.scope`, 'V_ENUM', 'scope.kind must be all | tags | deployments');
    if ((DEVELOPER_RESOURCES as readonly string[]).includes(res) && s.kind !== 'all') e(`${p}.scope`, 'V_ACCESS_SCOPE', `${res} needs scope all`);
    if (s.kind === 'tags') {
      if (!Array.isArray(s.tagSlugs) || s.tagSlugs.length < 1 || s.tagSlugs.length > 50) e(`${p}.scope.tagSlugs`, 'V_LENGTH', 'tagSlugs must have 1–50 items');
      else if (existingTags) for (const t of s.tagSlugs) if (!existingTags.includes(t)) e(`${p}.scope.tagSlugs`, 'V_REF', `tag "${t}" does not exist`);
    }
    if (s.kind === 'deployments') {
      if (!Array.isArray(s.deploymentIds) || s.deploymentIds.length < 1 || s.deploymentIds.length > 200) e(`${p}.scope.deploymentIds`, 'V_LENGTH', 'deploymentIds must have 1–200 items');
      else if (existingDeployments) for (const d of s.deploymentIds) if (!existingDeployments.includes(d)) e(`${p}.scope.deploymentIds`, 'V_REF', `deployment "${d}" does not exist`);
    }
  });
  return out;
}
