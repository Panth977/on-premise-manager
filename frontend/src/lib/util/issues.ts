import type { ValidationIssue } from '@opm/shared';

/** Issues at a path or below it (path + "." / "["). */
export function issuesUnder(issues: ValidationIssue[], prefix: string): ValidationIssue[] {
  if (!prefix) return issues;
  return issues.filter((i) => i.path === prefix || i.path.startsWith(prefix + '.') || i.path.startsWith(prefix + '['));
}
/** Error messages of exactly one path (or of a list of paths). */
export function errorsAt(issues: ValidationIssue[], ...paths: string[]): string[] {
  return issues.filter((i) => i.severity === 'error' && paths.includes(i.path)).map((i) => i.message);
}
export function warningsAt(issues: ValidationIssue[], ...paths: string[]): string[] {
  return issues.filter((i) => i.severity === 'warning' && paths.includes(i.path)).map((i) => i.message);
}
export function hasErrors(issues: ValidationIssue[]): boolean {
  return issues.some((i) => i.severity === 'error');
}
/** errors / warnings / infos. Info (round E, e.g. V_VAR_OPTIONAL_NULL) is advice only: never counted as a warning. */
export function countBy(issues: ValidationIssue[]) {
  return { errors: issues.filter((i) => i.severity === 'error').length, warnings: issues.filter((i) => i.severity === 'warning').length, infos: issues.filter((i) => i.severity === 'info').length };
}
/** text class of an issue line: error red, warning amber, info neutral */
export function sevClass(sev: ValidationIssue['severity']): string {
  return sev === 'error' ? 'text-red' : sev === 'warning' ? 'text-amber' : 'faint';
}
/** Re-base issues: strip a prefix from paths (e.g. "sites[app].config." → ""). */
export function rebase(issues: ValidationIssue[], prefix: string): ValidationIssue[] {
  return issues.map((i) => (i.path.startsWith(prefix) ? { ...i, path: i.path.slice(prefix.length) } : i));
}
export function issue(path: string, code: string, message: string, severity: ValidationIssue['severity'] = 'error'): ValidationIssue {
  return { path, code, message, severity };
}
/** merge client + server issues, dedupe by path+code+message */
export function mergeIssues(...lists: ValidationIssue[][]): ValidationIssue[] {
  const seen = new Set<string>();
  const out: ValidationIssue[] = [];
  for (const l of lists)
    for (const i of l) {
      const k = `${i.path}|${i.code}|${i.message}`;
      if (seen.has(k)) continue;
      seen.add(k);
      out.push(i);
    }
  return out;
}
