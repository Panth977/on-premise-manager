import { log } from './report.js';
export const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** poll fn until it returns a truthy value (or throw on timeout with the last value) */
export async function waitFor<T>(what: string, fn: () => Promise<T | null | undefined | false>, timeoutMs: number, everyMs = 2000): Promise<T> {
  const st = Date.now();
  let last: unknown = null;
  let lastErr: unknown = null;
  let n = 0;
  while (Date.now() - st < timeoutMs) {
    try {
      const v = await fn();
      if (v) {
        log(`waited ${((Date.now() - st) / 1000).toFixed(1)} s for ${what}`);
        return v as T;
      }
      last = v;
    } catch (e) {
      lastErr = e;
    }
    if (++n % 15 === 0) log(`… still waiting for ${what} (${((Date.now() - st) / 1000).toFixed(0)} s)${lastErr ? ' last error: ' + String(lastErr) : ''}`);
    await sleep(everyMs);
  }
  const err = new Error(`timeout after ${timeoutMs / 1000} s waiting for ${what}${lastErr ? `; last error: ${String(lastErr)}` : ''}${last !== null ? `; last value: ${JSON.stringify(last)?.slice(0, 300)}` : ''}`);
  throw err;
}

export async function http(url: string, init: RequestInit & { timeoutMs?: number } = {}): Promise<{ status: number; headers: Headers; text: string }> {
  const ctl = new AbortController();
  const t = setTimeout(() => ctl.abort(), init.timeoutMs ?? 15000);
  try {
    const r = await fetch(url, { redirect: 'manual', ...init, signal: ctl.signal });
    return { status: r.status, headers: r.headers, text: await r.text() };
  } finally {
    clearTimeout(t);
  }
}

export function errInfo(e: unknown): { status?: number; code?: string; message: string; details?: unknown } {
  const x = e as { status?: number; code?: string; message?: string; details?: unknown };
  return { status: x?.status, code: x?.code, message: x?.message ?? String(e), details: x?.details };
}

/** run fn expecting an OpmApiError; returns it (or null if fn succeeded) */
export async function expectError(fn: () => Promise<unknown>): Promise<{ status?: number; code?: string; message: string; details?: unknown } | null> {
  try {
    await fn();
    return null;
  } catch (e) {
    return errInfo(e);
  }
}
