/**
 * §21 #104 this site's branch (Branch page, sidebar badge, change bar): GET /_opm/api/branch (cloud head, local head,
 * ahead / behind, uncommitted counts, the running merge) and GET /_opm/api/branch/uncommitted (drift + notes).
 * §21 #105: "uncommitted" on the agent = ONLY changes made on the server outside the agent (drift); GUI edits live in
 * this browser (draft store). Polled by the app layout (15 s) and refreshed after every commit / merge.
 */
import { api } from '../api';
import { Resource } from './resource.svelte';

export const branch = new Resource(() => api.branchState({}));
export const uncommitted = new Resource(() => api.getUncommitted({}));

/** uncommitted on the agent = server changes (drift) + notes (§21 #105: `working` is always 0 from 0.14.1) */
export function uncommittedTotal(): number {
  const u = branch.data?.uncommitted;
  return u ? u.drift + u.notes : 0;
}
/** cloud commits not merged here */
export const behindCount = () => branch.data?.local.behind ?? 0;
export function refreshBranch() {
  branch.load();
  uncommitted.load();
}
