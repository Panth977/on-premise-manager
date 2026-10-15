import { authFetch, OpmApiError } from '../api';

/** Save a Blob as a file in the browser. */
export function saveBlob(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  a.rel = 'noopener';
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 30_000);
}

/** GET an authorised download endpoint (200 bytes or 302 to a signed URL) and save it. Throws OpmApiError on API errors. */
export async function downloadAuthorized(path: string, name: string): Promise<void> {
  const res = await authFetch(path);
  if (!res.ok) {
    let body: { error?: { code?: string; message?: string; details?: Record<string, unknown> } } | null = null;
    try {
      body = await res.json();
    } catch {
      /* not json */
    }
    throw new OpmApiError(res.status, body?.error?.code ?? 'internal', body?.error?.message ?? res.statusText, body?.error?.details ?? {});
  }
  saveBlob(await res.blob(), name);
}
