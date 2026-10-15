/**
 * Edge table helpers of the agent GUI Edge page (group badges, one-line match / action summaries). The edge FORMS (host,
 * route, ports, import, Test a URL) and their helpers are shared with the cloud console: $opm/forms/edge (SPEC §21 #102).
 */
import type { EdgeRoute } from '@opm/shared/types';
import type { EdgeOrderGroup } from '@opm/shared/edge-resolve';

export const GROUP_LABEL: Record<EdgeOrderGroup, string> = { exact: 'exact', 'prefix-stop': 'prefix ^~', regex: 'regex', prefix: 'prefix' };
export const GROUP_TONE: Record<EdgeOrderGroup, string> = { exact: 'accent', 'prefix-stop': 'warn', regex: 'info', prefix: '' };

export function matchText(r: EdgeRoute): string {
  const m = r.match;
  if (m.kind === 'exact') return `= ${m.path}`;
  if (m.kind === 'regex') return `${m.path.startsWith('(?i)') ? '~*' : '~'} ${m.path.replace(/^\(\?i\)/, '')}`;
  return `${m.stopRegex ? '^~ ' : ''}${m.path}`;
}

export function actionText(r: EdgeRoute): string {
  const a = r.action;
  if (a.type === 'redirect') return `redirect ${a.status} → ${a.to}${a.keepPath ? ' (+ path)' : ''}`;
  if (a.type === 'rewrite') return `rewrite ${a.regex} → ${a.replacement} (internal, re-matched)`;
  const path = a.rewrite ? ` · rewrite ${a.rewrite.regex} → ${a.rewrite.replacement}` : a.stripPrefix ? ' · strip' : '';
  const extra = [
    a.addPrefix ? `+${a.addPrefix}` : '',
    a.websocket ? 'ws' : '',
    !a.requestBuffering && !a.responseBuffering ? '' : `buffer ${a.requestBuffering ? 'req' : ''}${a.requestBuffering && a.responseBuffering ? '+' : ''}${a.responseBuffering ? 'resp' : ''}`,
    a.readTimeoutSec !== 60 ? `read ${a.readTimeoutSec}s` : '',
  ].filter(Boolean);
  return `proxy → ${a.containerSlug || '?'}:${a.portName || '?'}${path}${extra.length ? ' · ' + extra.join(' · ') : ''}`;
}
