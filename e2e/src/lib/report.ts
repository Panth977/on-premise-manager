// Step / check recorder → results/<ts>.md + RESULTS.md
import { mkdirSync, writeFileSync, copyFileSync, appendFileSync } from 'node:fs';
import { join, relative } from 'node:path';
import { CFG, E2E_DIR } from './env.js';

export type Status = 'pass' | 'fail' | 'skip' | 'warn';
interface Check { name: string; status: Status; detail: string }
interface Step { id: string; title: string; status: Status; ms: number; checks: Check[]; notes: string[]; shots: string[]; error?: string }

const ts = new Date().toISOString().replace(/[:.]/g, '-');
const steps: Step[] = [];
let cur: Step | null = null;
const t0 = Date.now();
export const RUN_TS = ts;
const logFile = join(CFG.runDir, `harness-${ts}.log`);
mkdirSync(CFG.runDir, { recursive: true });

export function log(...a: unknown[]): void {
  const line = `[${((Date.now() - t0) / 1000).toFixed(1).padStart(7)}s] ${a.map((x) => (typeof x === 'string' ? x : JSON.stringify(x))).join(' ')}`;
  console.log(line);
  appendFileSync(logFile, line + '\n');
}

export class CheckFailed extends Error {}

/** record a check; fatal=true throws (aborts the step) */
export function check(name: string, ok: boolean, detail: unknown = '', fatal = false): boolean {
  const d = typeof detail === 'string' ? detail : JSON.stringify(detail);
  const c: Check = { name, status: ok ? 'pass' : 'fail', detail: d.slice(0, 600) };
  (cur ?? ensureLoose()).checks.push(c);
  log(`${ok ? 'PASS' : 'FAIL'} ${name}${d ? ' — ' + d.slice(0, 300) : ''}`);
  if (!ok && fatal) throw new CheckFailed(`${name}: ${d}`);
  return ok;
}
export function warn(name: string, detail: unknown = ''): void {
  const d = typeof detail === 'string' ? detail : JSON.stringify(detail);
  (cur ?? ensureLoose()).checks.push({ name, status: 'warn', detail: d.slice(0, 600) });
  log(`WARN ${name} — ${d.slice(0, 300)}`);
}
export function note(s: string): void {
  (cur ?? ensureLoose()).notes.push(s);
  log(`note: ${s}`);
}
export function shot(path: string): void {
  (cur ?? ensureLoose()).shots.push(path);
}
function ensureLoose(): Step {
  cur = { id: '-', title: 'setup', status: 'pass', ms: 0, checks: [], notes: [], shots: [] };
  steps.push(cur);
  return cur;
}

const failedSteps = new Set<string>();
/** run a step; deps = steps that must have passed (else skipped) */
export async function step(id: string, title: string, fn: () => Promise<void>, deps: string[] = []): Promise<boolean> {
  const s: Step = { id, title, status: 'pass', ms: 0, checks: [], notes: [], shots: [] };
  steps.push(s);
  cur = s;
  const n = Number.parseFloat(id);
  if (n > CFG.untilStep) {
    s.status = 'skip';
    s.notes.push(`skipped (OPM_E2E_UNTIL=${CFG.untilStep})`);
    failedSteps.add(id);
    return false;
  }
  const missing = deps.filter((d) => failedSteps.has(d));
  if (missing.length) {
    s.status = 'skip';
    s.notes.push(`skipped: depends on failed step(s) ${missing.join(', ')}`);
    log(`SKIP step ${id} ${title} (needs ${missing.join(', ')})`);
    failedSteps.add(id);
    write();
    return false;
  }
  log(`==== step ${id}: ${title}`);
  const st = Date.now();
  try {
    await fn();
  } catch (err) {
    s.error = err instanceof Error ? `${err.name}: ${err.message}` + (err instanceof CheckFailed ? '' : `\n${(err.stack ?? '').split('\n').slice(1, 4).join('\n')}`) : String(err);
    const det = (err as { details?: unknown }).details;
    if (det) s.error += `\ndetails: ${JSON.stringify(det).slice(0, 1500)}`;
    log(`ERROR step ${id}: ${s.error}`);
  }
  s.ms = Date.now() - st;
  if (s.error || s.checks.some((c) => c.status === 'fail')) s.status = 'fail';
  else if (s.checks.some((c) => c.status === 'warn')) s.status = 'warn';
  // dependents are skipped only when the step aborted (exception / fatal check); failed non-fatal checks are reported but do not block
  if (s.error) failedSteps.add(id);
  log(`==== step ${id} ${s.status.toUpperCase()} (${(s.ms / 1000).toFixed(1)} s)`);
  cur = null;
  write();
  return s.status !== 'fail';
}

export const extra: { title: string; body: string }[] = [];

export function write(): string {
  mkdirSync(CFG.resultsDir, { recursive: true });
  const icon: Record<Status, string> = { pass: 'PASS', fail: 'FAIL', skip: 'SKIP', warn: 'WARN' };
  const total = steps.filter((s) => s.id !== '-');
  const lines: string[] = [];
  lines.push(`# OPM hello-world e2e — results ${ts}`, '');
  lines.push(`- API: \`${CFG.apiBase}\` · frontend: \`${CFG.frontendUrl}\` · auth emulator: \`${CFG.authEmulator || 'no'}\``);
  lines.push(`- OPM_APPROVE_WAIT_SEC=${CFG.approveWaitSec} · UI flows: ${CFG.ui ? 'on' : 'off'} · duration ${((Date.now() - t0) / 1000).toFixed(0)} s`);
  lines.push(`- steps: ${total.filter((s) => s.status === 'pass').length} pass · ${total.filter((s) => s.status === 'warn').length} warn · ${total.filter((s) => s.status === 'fail').length} fail · ${total.filter((s) => s.status === 'skip').length} skip`, '');
  lines.push('| step | title | result | time |', '|---|---|---|---|');
  for (const s of total) lines.push(`| ${s.id} | ${s.title} | ${icon[s.status]} | ${(s.ms / 1000).toFixed(1)} s |`);
  lines.push('');
  for (const s of steps) {
    lines.push(`## ${s.id} ${s.title} — ${icon[s.status]}`, '');
    if (s.error) lines.push('```', s.error, '```', '');
    for (const c of s.checks) lines.push(`- ${icon[c.status]} ${c.name}${c.detail ? ` — \`${c.detail.replace(/`/g, "'").replace(/\n/g, ' ')}\`` : ''}`);
    for (const n of s.notes) lines.push(`- note: ${n}`);
    for (const p of s.shots) lines.push('', `![${p}](${relative(E2E_DIR, p)})`);
    lines.push('');
  }
  for (const x of extra) lines.push(`## ${x.title}`, '', x.body, '');
  const file = join(CFG.resultsDir, `${ts}.md`);
  writeFileSync(file, lines.join('\n'));
  copyFileSync(file, join(E2E_DIR, 'RESULTS.md'));
  return file;
}
export function summary(): { pass: number; fail: number; skip: number; warn: number } {
  const total = steps.filter((s) => s.id !== '-');
  return {
    pass: total.filter((s) => s.status === 'pass').length,
    fail: total.filter((s) => s.status === 'fail').length,
    skip: total.filter((s) => s.status === 'skip').length,
    warn: total.filter((s) => s.status === 'warn').length,
  };
}
