/**
 * Where the bytes of an uploaded icon ("upload:<iconId>") live.
 * Real API: the public URL `${apiBase}/v1/icons/{id}/content` (no auth, immutable cache).
 * Mock mode: the fake backend keeps a data: URL per icon (src/lib/mock/db.ts mockIconDataUrl).
 */
import { MOCK, apiBase } from '../env';

export function iconContentUrl(id: string): string {
  return `${apiBase()}/v1/icons/${encodeURIComponent(id)}/content`;
}

const mockCache = new Map<string, string | null>();
/** Resolve the <img> src of an uploaded icon (sync URL on the real API; data URL from the mock). */
export async function resolveIconSrc(id: string): Promise<string | null> {
  if (!MOCK) return iconContentUrl(id);
  if (mockCache.has(id)) return mockCache.get(id)!;
  const m = await import('../mock/db');
  const url = m.mockIconDataUrl(id);
  if (url) mockCache.set(id, url);
  return url;
}
/** Sync variant: the real URL, or a cached mock data URL (null = must resolve async). */
export function iconSrcSync(id: string): string | null {
  if (!MOCK) return iconContentUrl(id);
  return mockCache.get(id) ?? null;
}
