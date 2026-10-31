import { allowed, canSeeDeployment, type Account, type Action, type Resource, type AccessTarget } from '@opm/shared';
import { col } from './fire.js';
import { E } from './errors.js';
import { decodeDeployment, type StoredDeployment } from './codec.js';
import type { Ctx } from './router.js';

export const targetOf = (d: Pick<StoredDeployment, 'deploymentId' | 'tagSlugs' | 'managerAccountUids'>): AccessTarget => ({
  deploymentId: d.deploymentId,
  tagSlugs: d.tagSlugs ?? [],
  managerAccountUids: d.managerAccountUids ?? [],
});

export function need(c: Ctx, resource: Resource, action: Action, target: AccessTarget | null = null) {
  if (!allowed(c.account, resource, action, target)) throw E.forbidden(`needs ${resource}.${action}${target ? ` on deployment ${target.deploymentId}` : ''}`);
}
export const has = (c: Ctx, resource: Resource, action: Action, target: AccessTarget | null = null) => allowed(c.account, resource, action, target);

/** Load a deployment for a human endpoint: deleted or invisible → 404. */
export async function loadDep(c: Ctx, id = c.params.deploymentId!): Promise<StoredDeployment> {
  const snap = await col.deployments().doc(id).get();
  if (!snap.exists) throw E.notFound('deployment not found');
  const d = decodeDeployment(snap.data()!);
  if (d.deleted || !canSeeDeployment(c.account, targetOf(d))) throw E.notFound('deployment not found');
  return d;
}
export function needDep(c: Ctx, d: StoredDeployment, resource: Resource, action: Action) {
  need(c, resource, action, targetOf(d));
}

/** "deployment.config.write on any deployment" (blobs, uploads §5.2): a rule with that resource+action, or a manager. */
export async function hasOnAnyDeployment(account: Account, resource: Resource, action: Action): Promise<boolean> {
  if (account.disabled) return false;
  if (account.access.some((r) => r.resource === resource && r.actions.includes(action))) return true;
  const q = await col.deployments().where('managerAccountUids', 'array-contains', account.uid).limit(5).get();
  return q.docs.some((d) => !d.data().deleted);
}
