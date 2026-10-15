/** §21 #90 agent upgrade helpers shared by the Setup + Status pages (cloud console). */
import type { AgentUpgradePhase, AgentUpgradeProgress, AgentUpgradeRun, AgentUpgradeRunSiteStatus, SiteAgentUpgradeView } from '@opm/shared';

/** the phase stepper, in order */
export const UPGRADE_STEPS: { phase: AgentUpgradePhase; label: string }[] = [
  { phase: 'download', label: 'Download' },
  { phase: 'verify', label: 'Verify' },
  { phase: 'swap', label: 'Swap' },
  { phase: 'restart', label: 'Restart' },
  { phase: 'health', label: 'Health' },
  { phase: 'done', label: 'Done' },
];
export const ACTIVE_PHASES: ReadonlySet<AgentUpgradePhase> = new Set(['queued', 'download', 'verify', 'swap', 'restart', 'health']);
export const isActivePhase = (p: AgentUpgradePhase | null | undefined): boolean => !!p && ACTIVE_PHASES.has(p);

export type UpgradeState = 'none' | 'queued' | 'running' | 'done' | 'failed' | 'rolled-back';
/** What the site is doing now: a queued request the agent has not started, a running attempt, or the last result. */
export function upgradeState(v: SiteAgentUpgradeView | null | undefined): UpgradeState {
  if (!v) return 'none';
  const p = v.progress;
  if (v.request && (!p || p.requestId !== v.request.requestId)) return 'queued';
  if (p && isActivePhase(p.phase)) return 'running';
  if (p?.phase === 'done') return 'done';
  if (p?.phase === 'failed') return 'failed';
  if (p?.phase === 'rolled-back') return 'rolled-back';
  return 'none';
}
/** true while the page should poll fast (a request is queued or an upgrade / run is going) */
export function upgradeBusy(views: (SiteAgentUpgradeView | null | undefined)[], run: AgentUpgradeRun | null | undefined): boolean {
  if (run?.status === 'running') return true;
  return views.some((v) => {
    const s = upgradeState(v);
    return s === 'queued' || s === 'running';
  });
}

/**
 * The step a finished-but-failed attempt stopped at: progress.failedPhase when the agent reports it, else inferred:
 * rolled-back → health; a failed verification check → verify; an incomplete download → download; else swap.
 */
export function failedStep(p: AgentUpgradeProgress): AgentUpgradePhase {
  if (p.failedPhase && UPGRADE_STEPS.some((x) => x.phase === p.failedPhase)) return p.failedPhase;
  if (p.phase === 'rolled-back') return 'health';
  const v = p.verification;
  if (v && (v.signatureOk === false || v.sha256Ok === false || v.selfTestOk === false || v.error)) return 'verify';
  if (!v || (p.bytesTotal !== null && p.bytesDone < p.bytesTotal)) return 'download';
  return 'swap';
}
export type StepState = 'done' | 'current' | 'failed' | 'todo';
export function stepStates(p: AgentUpgradeProgress): StepState[] {
  const idx = (ph: AgentUpgradePhase) => UPGRADE_STEPS.findIndex((s) => s.phase === ph);
  if (p.phase === 'done') return UPGRADE_STEPS.map(() => 'done');
  if (p.phase === 'failed' || p.phase === 'rolled-back') {
    const f = idx(failedStep(p));
    return UPGRADE_STEPS.map((_, i) => (i < f ? 'done' : i === f ? 'failed' : 'todo'));
  }
  const c = p.phase === 'queued' ? -1 : idx(p.phase);
  return UPGRADE_STEPS.map((_, i) => (i < c ? 'done' : i === c ? 'current' : 'todo'));
}
export const PHASE_LABEL: Record<AgentUpgradePhase, string> = {
  queued: 'Queued',
  download: 'Downloading',
  verify: 'Verifying',
  swap: 'Swapping the binary',
  restart: 'Restarting',
  health: 'Waiting for the health check',
  done: 'Done',
  failed: 'Failed',
  'rolled-back': 'Rolled back',
};
export const RUN_SITE_LABEL: Record<AgentUpgradeRunSiteStatus, string> = {
  pending: 'pending',
  requested: 'requested',
  running: 'running',
  done: 'done',
  failed: 'failed',
  skipped: 'skipped',
  cancelled: 'cancelled',
};
export const RUN_SITE_TONE: Record<AgentUpgradeRunSiteStatus, string> = {
  pending: '',
  requested: 'blue',
  running: 'blue',
  done: 'green',
  failed: 'red',
  skipped: 'amber',
  cancelled: '',
};
export const RUN_TONE: Record<AgentUpgradeRun['status'], string> = { running: 'blue', done: 'green', failed: 'red', cancelled: '' };

/** "sudo opm-agent upgrade" exists from agent 0.8 (§21 #86) */
export function hasCliUpgrade(version: string | null | undefined): boolean {
  const m = /^v?(\d+)\.(\d+)\./.exec(version ?? '');
  if (!m) return false;
  const [maj, min] = [Number(m[1]), Number(m[2])];
  return maj > 0 || min >= 8;
}
