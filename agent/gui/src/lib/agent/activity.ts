/**
 * Labels, icons, tones and a one-line data summary for the §21 #90 activity types (agent.upgrade-*, agent.restarted, …).
 * Used by the Activities table + drawer and the Agent page history. Unknown types → null (the page shows the raw type).
 */
export interface ActivityMeta {
  label: string;
  icon: string;
  tone: '' | 'ok' | 'warn' | 'bad' | 'info' | 'accent';
  /** key facts of `data`, in order, for the drawer */
  facts: [string, string][];
}

type D = Record<string, unknown>;
const str = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : typeof v === 'string' ? v : JSON.stringify(v));
const arrow = (d: D) => `${str(d.from)} → ${str(d.to)}`;
const VIA: Record<string, string> = { gui: 'agent GUI', cloud: 'cloud console', cli: 'opm-agent upgrade (CLI)', mesh: 'upgrade all (mesh)' };
const via = (d: D) => VIA[String(d.via)] ?? str(d.via);

export function agentActivityMeta(type: string, data: D | null | undefined): ActivityMeta | null {
  const d = data ?? {};
  switch (type) {
    case 'agent.upgrade-started':
      return { label: 'Upgrade started', icon: 'download', tone: 'info', facts: [['Version', arrow(d)], ['Via', via(d)], ['Upgrade id', str(d.upgradeId)], ...(d.requestId ? [['Cloud request', str(d.requestId)] as [string, string]] : []), ...(d.runId ? [['Run', str(d.runId)] as [string, string]] : [])] };
    case 'agent.upgraded':
      return { label: 'Agent upgraded', icon: 'check', tone: 'ok', facts: [['Version', arrow(d)], ['Took', d.durationSec != null ? `${d.durationSec} s` : '—'], ['Via', via(d)], ['Upgrade id', str(d.upgradeId)]] };
    case 'agent.upgrade-failed':
      return { label: 'Upgrade failed', icon: 'alert', tone: 'bad', facts: [['Version', arrow(d)], ['Failed in', str(d.phase)], ['Error', str(d.error)], ['Via', via(d)], ['Upgrade id', str(d.upgradeId)]] };
    case 'agent.rolled-back':
      return { label: 'Rolled back', icon: 'history', tone: 'warn', facts: [['Failed version', str(d.from)], ['Restored', str(d.to)], ['By', d.by === 'guard' ? 'boot guard (systemd ExecStartPre)' : d.by === 'watcher' ? 'upgrade watcher' : str(d.by)], ['Reason', str(d.reason)], ['Upgrade id', str(d.upgradeId)]] };
    case 'agent.restarted':
      return { label: 'Agent restarted', icon: 'rotate', tone: '', facts: [['Reason', str(d.reason)], ['Via', d.via === 'exec' ? 're-exec (no systemd)' : str(d.via)]] };
    case 'agent.config-changed': {
      const ch = Array.isArray(d.changed) ? (d.changed as string[]) : [];
      const before = (d.before ?? {}) as D;
      const after = (d.after ?? {}) as D;
      return { label: 'agent.json changed', icon: 'sliders', tone: 'accent', facts: [['Changed', ch.join(', ') || '—'], ...ch.map((k) => [k, `${str(before[k])} → ${str(after[k])}`] as [string, string])] };
    }
    case 'agent.support-bundle-downloaded':
      return { label: 'Support bundle downloaded', icon: 'archive', tone: '', facts: [['Size', typeof d.sizeBytes === 'number' ? `${Math.round(d.sizeBytes / 1024)} KiB` : '—'], ['Sections', Array.isArray(d.sections) ? (d.sections as string[]).join(', ') : '—']] };
    case 'agent.upgrade-requested':
      return { label: 'Upgrade requested (cloud)', icon: 'send', tone: 'info', facts: [['Site', str(d.siteSlug)], ['Version', str(d.version)], ['Request', str(d.requestId)], ...(d.runId ? [['Run', str(d.runId)] as [string, string]] : [])] };
    // §21 #110 a person stopped a running update (nothing rolled back) / the roll back to the pre-update backup
    case 'update.terminated':
      return { label: 'Update stopped', icon: 'stop', tone: 'warn', facts: [['Site', str(d.siteSlug)], ['Reason', str(d.reason)], ['Update id', str(d.updateId)]] };
    case 'update.recovered':
      return { label: 'Rolled back to backup', icon: 'history', tone: d.error ? 'bad' : 'warn', facts: [['Site', str(d.siteSlug)], ['Backup', str(d.snapshotId)], ['Reason', str(d.reason)], ['Took', typeof d.durationMs === 'number' ? `${Math.round(d.durationMs / 1000)} s` : '—'], ...(d.error ? [['Error', str(d.error)] as [string, string]] : []), ...(d.updateId ? [['Update id', str(d.updateId)] as [string, string]] : []), ...(d.via ? [['Via', via(d)] as [string, string]] : [])] };
    case 'agent.upgrade-cancelled':
      return { label: 'Upgrade cancelled (cloud)', icon: 'x', tone: 'warn', facts: [['Site', str(d.siteSlug)], ['Request', str(d.requestId)], ['Run', str(d.runId)]] };
  }
  return null;
}
