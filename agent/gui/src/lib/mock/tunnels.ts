/**
 * §21 #125 mock of the port tunnels of a site (PORT_TUNNELS_AGENT_API): the tunnels come from the cloud view, each is off
 * on its site until switched on here. State lives in page memory (a reload starts from the seeds).
 *
 * Seeds: app (this site) — hello-api · http on 2001 (on, used), edge https on 2002 (off, last error); db — server
 * port 5432 on 2003 (off); northwind — none.
 * Knobs (localStorage): opm.mock.tunnels=off → the cloud does not carry tunnels (cloudTunnels false, no rows);
 * opm.mock.tunnels=empty → the cloud carries tunnels but none was added;
 * a session with `remote` (opm.mock.session) works through the cloud → switching ON is refused (403 forbidden).
 */
import type { SitePortTunnel, SitePortTunnelsResponse } from '@opm/shared/tunnels';
import { portTunnelTargetText } from '@opm/shared/tunnels';
import type { ErrorCode } from '@opm/shared/types';
import * as D from './data';

type Json = Record<string, unknown>;
interface Ctx {
  params: Record<string, string>;
  body: unknown;
}
export interface TunnelsDeps {
  fail: (code: ErrorCode, message: string, details?: Json) => never;
  needSite: (site: string, allowStar: boolean) => void;
  needReach: (site: string) => void;
  addActivity: (site: string, type: string, summary: string, actor: string | null, data?: Json) => void;
  /** the session works through the cloud (§21 #118) */
  isRemote: (s: unknown) => boolean;
  /** opm.mock.tunnels: 'off' (the cloud does not carry tunnels) | 'empty' (none added) | null */
  mode: () => string | null;
}
type TunnelsHandler = (c: Ctx, s: unknown, actor: string | null) => unknown;

function seed(): SitePortTunnel[] {
  const t = (tunnelId: string, siteSlug: string, target: SitePortTunnel['target'], publicPort: number, label: string | null, extra: Partial<SitePortTunnel> = {}): SitePortTunnel => ({
    tunnelId,
    siteSlug,
    target,
    publicPort,
    label,
    createdAt: D.ago(86400 * 2),
    createdBy: 'owner@acme.example',
    enabled: false,
    openConnections: 0,
    lastConnectionAt: null,
    lastError: null,
    ...extra,
  });
  return [
    t('tun-api', D.SELF, { kind: 'container', containerSlug: 'hello-api', portName: 'http' }, 2001, 'API for the integration partner', { enabled: true, openConnections: 2, lastConnectionAt: D.ago(40) }),
    t('tun-edge', D.SELF, { kind: 'edge', scheme: 'https' }, 2002, null, { lastConnectionAt: D.ago(3600 * 5), lastError: 'dial tcp 127.0.0.1:443: connect: connection refused' }),
    t('tun-pg', 'db', { kind: 'host', port: 5432 }, 2003, 'Postgres (read-only reports)'),
  ];
}

export function createTunnelsMock(d: TunnelsDeps): Record<'sitePortTunnels' | 'siteSetPortTunnel', TunnelsHandler> {
  const all = seed();
  const remoteGui = (site: string) => site !== 'db'; // db: its remote GUI is switched off
  const listOf = (site: string): SitePortTunnelsResponse => {
    const m = d.mode();
    return { siteSlug: site, cloudTunnels: m !== 'off', remoteGui: remoteGui(site), items: m ? [] : all.filter((t) => t.siteSlug === site).map((t) => ({ ...t })) };
  };
  return {
    sitePortTunnels: (c) => {
      const site = c.params.siteSlug;
      d.needSite(site, false);
      d.needReach(site);
      return listOf(site);
    },
    siteSetPortTunnel: (c, s, actor) => {
      const site = c.params.siteSlug;
      d.needSite(site, false);
      d.needReach(site);
      const b = c.body as { enabled?: unknown } | null;
      if (typeof b?.enabled !== 'boolean') d.fail('invalid', 'body {enabled: boolean} is required');
      const enabled = b!.enabled as boolean;
      const t = d.mode() ? undefined : all.find((x) => x.siteSlug === site && x.tunnelId === c.params.tunnelId);
      if (!t) d.fail('not-found', `site ${site} has no tunnel ${c.params.tunnelId}`);
      if (enabled && d.isRemote(s)) d.fail('forbidden', 'switch a tunnel on on the site itself, not through the cloud');
      if (t!.enabled !== enabled) {
        t!.enabled = enabled;
        if (!enabled) t!.openConnections = 0;
        d.addActivity(site, enabled ? 'tunnel.enabled' : 'tunnel.disabled', `Tunnel ${portTunnelTargetText(t!.target)} on port ${t!.publicPort} switched ${enabled ? 'on' : 'off'}`, actor, { tunnelId: t!.tunnelId, target: t!.target, publicPort: t!.publicPort });
      }
      return { ...t! };
    },
  };
}
