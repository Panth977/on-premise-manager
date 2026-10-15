import type { ValidationIssue } from '@opm/shared';

/** Map a validation issue path of a deployment draft to the page that edits the field. */
export function draftIssueLink(deploymentId: string, i: Pick<ValidationIssue, 'path'>): string | null {
  const base = `/app/deployments/${deploymentId}`;
  const p = i.path ?? '';
  let m: RegExpExecArray | null;
  if (p.startsWith('variables')) return `${base}/variables`;
  if (p.startsWith('secrets')) return `${base}/secrets`;
  if (p.startsWith('files')) return `${base}/files`;
  if ((m = /^sites\[([^\]]+)\]\.config\.containers\[([^\]]+)\]/.exec(p))) {
    const key = m[2]!;
    return `${base}/sites/${m[1]}/containers/${key}`;
  }
  if ((m = /^sites\[([^\]]+)\]\.config\.devTools/.exec(p))) return `${base}/sites/${m[1]}/devtools`;
  if ((m = /^sites\[([^\]]+)\]\.config\.(containers|edge|volumes)/.exec(p))) return `${base}/sites/${m[1]}/${m[2]}`;
  if ((m = /^sites\[([^\]]+)\]/.exec(p))) return `${base}/sites/${m[1]}/settings`;
  return `${base}/sites`;
}
