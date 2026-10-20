/**
 * §21 #101 persist-to-cloud volumes — the agent's VOLUME_SYNC_API client (same rules as lib/tools/api.ts: same origin,
 * cookie session, Authorization: Actor <token> on session+actor endpoints, the lib/api 401 handling) + GUI helpers.
 */
import { createVolumeSyncClient, VOLUME_SYNC_STATE_LABEL, type VolumeSnapshotTrigger, type VolumeSyncState } from '@opm/shared/volume-content';
import { onApiError, opmFetch } from '$lib/api';
import { session } from '$lib/state/session.svelte';

// createVolumeSyncClient takes a raw AuthProvider (not a token getter like createToolsClient): Actor header on
// session+actor endpoints only, cookie same-origin
const client = createVolumeSyncClient(
  '',
  ({ endpoint }) => {
    if (endpoint.auth !== 'session+actor') return null;
    const t = session.actorToken;
    return t ? `Actor ${t}` : null;
  },
  { fetch: opmFetch, credentials: 'same-origin' },
);
type AnyCall = (a: unknown) => Promise<unknown>;
const raw = client as unknown as Record<string, AnyCall>;
export const vsync: typeof client = new Proxy({} as typeof client, {
  get(_t, name: string) {
    return async (args: unknown = {}) => {
      try {
        return await raw[name](args ?? {});
      } catch (e) {
        onApiError(name, e);
        throw e;
      }
    };
  },
});

/** §21 #117 the state compares the live folder with the committed content (agents ≥ 0.19 answer in-sync / live-changed / error) */
export const SYNC_LABEL: Record<VolumeSyncState, string> = {
  ...VOLUME_SYNC_STATE_LABEL,
  'in-sync': 'Matches the commit',
  'live-changed': 'Changed on the server',
};
/** badge tone per sync state (app.css .badge tones) */
export const SYNC_TONE: Record<VolumeSyncState, 'ok' | 'warn' | 'bad' | 'info' | 'accent' | ''> = {
  'in-sync': 'ok',
  'live-changed': 'warn',
  'cloud-ahead': 'accent',
  conflict: 'bad',
  'never-snapshotted': '',
  'pending-upload': 'info',
  error: 'bad',
};
export const SYNC_HINT: Record<VolumeSyncState, string> = {
  'in-sync': "The live folder equals the content this site's head commits.",
  'live-changed': 'Files changed on the server since the last commit — not committed yet (Branch page).',
  'cloud-ahead': 'An older agent state: the cloud sent a version (agents ≥ 0.19 take content with the merge).',
  conflict: 'An older agent state: the cloud sent a version that conflicts with the live folder.',
  'never-snapshotted': 'Not scanned yet.',
  'pending-upload': 'An older agent state: snapshots wait for the cloud.',
  error: 'The last scan failed.',
};
export const TRIGGER_LABEL: Record<VolumeSnapshotTrigger, string> = {
  'manual-agent': 'Snapshot now (site)',
  'manual-cloud': 'requested by the cloud',
  auto: 'automatic',
  'before-apply': 'before applying',
  'after-apply': 'after applying',
};

export const shortHash = (h: string | null | undefined) => (h ? h.replace(/^sha256:/, '').slice(0, 12) : '—');
