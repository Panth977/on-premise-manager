/**
 * §21 #101 persist-to-cloud volumes — the agent's VOLUME_SYNC_API client (same rules as lib/tools/api.ts: same origin,
 * cookie session, Authorization: Actor <token> on session+actor endpoints, the lib/api 401 handling) + GUI helpers.
 */
import { createVolumeSyncClient, looksLikeText, VOLUME_CONTENT_LIMITS, VOLUME_SYNC_STATE_LABEL, type VolumeSnapshotTrigger, type VolumeSyncState } from '@opm/shared/volume-content';
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

export const SYNC_LABEL = VOLUME_SYNC_STATE_LABEL;
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
  'in-sync': 'The live volume equals the last snapshot in the cloud.',
  'live-changed': 'Files changed on the site since the last snapshot. An automatic snapshot uploads them after 2 quiet minutes (or use Snapshot now).',
  'cloud-ahead': 'The cloud sent a newer version that applies without a choice: the live volume has not changed since, or only files the cloud did not touch changed (they are kept).',
  conflict: 'The cloud sent a version, and some files changed both in the cloud and on the site. Applying it asks how to merge (overwrite / keep live / per file).',
  'never-snapshotted': 'Persist to cloud is on, but no snapshot was taken yet.',
  'pending-upload': 'Snapshots wait for the cloud (store-and-forward: they upload when the cloud is reachable).',
  error: 'The last scan or snapshot failed.',
};
export const TRIGGER_LABEL: Record<VolumeSnapshotTrigger, string> = {
  'manual-agent': 'Snapshot now (site)',
  'manual-cloud': 'requested by the cloud',
  auto: 'automatic',
  'before-apply': 'before applying',
  'after-apply': 'after applying',
};

/** One side of the plan for the diff viewer: text (≤ editor limit and looks like text) or binary / too large. */
export type SideContent = { kind: 'text'; text: string; size: number } | { kind: 'binary'; size: number } | { kind: 'large'; size: number } | { kind: 'absent' };
export async function fetchSide(siteSlug: string, volumeSlug: string, side: 'live' | 'cloud' | 'base', path: string, knownSize: number | null): Promise<SideContent> {
  if (knownSize === null) return { kind: 'absent' };
  if (knownSize > VOLUME_CONTENT_LIMITS.editorTextMaxBytes) return { kind: 'large', size: knownSize };
  const res = await vsync.volumeSyncFile({ siteSlug, volumeSlug, query: { side, path } });
  const buf = new Uint8Array(await res.arrayBuffer());
  if (!looksLikeText(buf)) return { kind: 'binary', size: buf.length };
  return { kind: 'text', text: new TextDecoder().decode(buf), size: buf.length };
}
export const shortHash = (h: string | null | undefined) => (h ? h.replace(/^sha256:/, '').slice(0, 12) : '—');
