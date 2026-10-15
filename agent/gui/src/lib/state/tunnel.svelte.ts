/**
 * Browser internet tunnel worker (SPEC §9.10). Lives for the whole tab (not only the Internet page):
 * long-poll GET /_opm/api/tunnel/next → fetch(url) to the cloud from this browser → POST tunnel/result
 * (≤ 4 MiB inline) or chunked: result{chunked:true} → chunk 0..n-1 (≤ 4 MiB each, in order) → complete{parts, sha256}.
 * At most 4 requests in flight. 409 on any tunnel call = the lease ended (revoked by another tab / expired).
 */
import type { TunnelLease, TunnelRequest } from '@opm/shared/types';
import { api, errCode, errMessage, isApiError, opmFetch } from '../api';
import { b64decode, b64encode, sha256Hex } from '../bytes';

const MAX_INFLIGHT = 4;
const INLINE_MAX = 4 * 1024 * 1024;
const CHUNK = 4 * 1024 * 1024;

export interface TunnelLogRow {
  reqId: string;
  method: string;
  path: string;
  step: 'fetching' | 'sending' | 'chunks' | 'done' | 'failed';
  status: number | null;
  bytes: number;
  ms: number | null;
  error: string | null;
  at: number;
}

class TunnelWorker {
  lease = $state<TunnelLease | null>(null);
  running = $state(false);
  inFlight = $state(0);
  requests = $state(0);
  bytes = $state(0);
  lastError = $state<string | null>(null);
  ended = $state<string | null>(null);
  log = $state<TunnelLogRow[]>([]);
  #slot: (() => void) | null = null;

  get active() {
    return this.running && !!this.lease;
  }

  async start(): Promise<void> {
    this.ended = null;
    const r = await api.tunnelStart({ body: { userAgent: navigator.userAgent } });
    this.lease = r.lease;
    this.requests = 0;
    this.bytes = 0;
    this.lastError = null;
    this.log = [];
    this.running = true;
    void this.#loop();
  }

  async stop(): Promise<void> {
    const id = this.lease?.leaseId;
    this.running = false;
    this.#slot?.();
    if (id) {
      try {
        await api.tunnelStop({ body: { leaseId: id } });
      } finally {
        this.lease = null;
        this.ended = 'Stopped in this tab.';
      }
    }
  }

  #end(reason: string) {
    if (!this.running) return;
    this.running = false;
    this.lease = null;
    this.ended = reason;
    this.#slot?.();
  }

  #row(req: TunnelRequest): TunnelLogRow {
    let path = req.url;
    try {
      const u = new URL(req.url);
      path = u.pathname + u.search;
    } catch {
      /* keep url */
    }
    const row: TunnelLogRow = { reqId: req.reqId, method: req.method, path, step: 'fetching', status: null, bytes: 0, ms: null, error: null, at: Date.now() };
    this.log = [row, ...this.log].slice(0, 50);
    return this.log[0];
  }

  async #loop() {
    let backoff = 1000;
    while (this.running && this.lease) {
      if (this.inFlight >= MAX_INFLIGHT) {
        await new Promise<void>((r) => (this.#slot = r));
        this.#slot = null;
        continue;
      }
      const leaseId = this.lease.leaseId;
      try {
        const r = await api.tunnelNext({ query: { leaseId, max: MAX_INFLIGHT - this.inFlight } });
        backoff = 1000;
        for (const req of r.requests) void this.#handle(leaseId, req);
      } catch (e) {
        if (errCode(e) === 'conflict') return this.#end('The lease ended: another tab revoked it, or it expired (no poll for 30 s).');
        if (isApiError(e) && e.status === 401) return this.#end('The session ended; the tunnel stopped.');
        this.lastError = errMessage(e);
        await new Promise((r) => setTimeout(r, backoff));
        backoff = Math.min(backoff * 2, 15_000);
      }
    }
  }

  async #handle(leaseId: string, req: TunnelRequest) {
    this.inFlight++;
    const row = this.#row(req);
    const t0 = performance.now();
    try {
      let status = 0;
      let headers: Record<string, string> = {};
      let body: Uint8Array | null = null;
      let error: string | null = null;
      try {
        const ctl = new AbortController();
        const to = setTimeout(() => ctl.abort(), Math.max(5, req.timeoutSec) * 1000);
        const res = await opmFetch(req.url, {
          method: req.method,
          headers: req.headers,
          body: req.bodyB64 ? (b64decode(req.bodyB64) as BodyInit) : undefined,
          mode: 'cors',
          credentials: 'omit',
          redirect: 'follow',
          signal: ctl.signal,
        });
        clearTimeout(to);
        status = res.status;
        res.headers.forEach((v, k) => (headers[k] = v));
        body = new Uint8Array(await res.arrayBuffer());
      } catch (e) {
        status = 0;
        headers = {};
        body = null;
        error = e instanceof Error ? `${e.name}: ${e.message}` : String(e);
      }
      row.status = status;
      row.bytes = body?.length ?? 0;
      row.step = 'sending';
      if (!body || body.length <= INLINE_MAX) {
        await api.tunnelResult({ body: { leaseId, reqId: req.reqId, status, headers, bodyB64: body && body.length ? b64encode(body) : null, chunked: false, error } });
      } else {
        await api.tunnelResult({ body: { leaseId, reqId: req.reqId, status, headers, bodyB64: null, chunked: true, error: null } });
        row.step = 'chunks';
        const parts = Math.ceil(body.length / CHUNK);
        for (let i = 0; i < parts; i++) {
          if (!this.running) throw new Error('stopped');
          await api.tunnelChunk({ reqId: req.reqId, query: { leaseId, index: i }, body: body.subarray(i * CHUNK, Math.min(body.length, (i + 1) * CHUNK)) });
        }
        await api.tunnelComplete({ reqId: req.reqId, body: { leaseId, parts, sha256: `sha256:${sha256Hex(body)}` } });
      }
      row.step = error ? 'failed' : 'done';
      row.error = error;
      if (error) this.lastError = error;
      this.requests++;
      this.bytes += body?.length ?? 0;
    } catch (e) {
      row.step = 'failed';
      row.error = errMessage(e);
      this.lastError = row.error;
      if (errCode(e) === 'conflict') this.#end('The lease ended: another tab revoked it, or it expired.');
    } finally {
      row.ms = Math.round(performance.now() - t0);
      this.log = [...this.log];
      this.inFlight--;
      this.#slot?.();
    }
  }
}

export const tunnel = new TunnelWorker();
