/**
 * "Restarting the agent…" (round I, SPEC §21 #90 d). While this site's agent restarts (upgrade, Restart agent,
 * agent.json save) the GUI polls GET /_opm/api/health (§21 #91; /info on agents without it) every 2 s for up to 120 s, tolerating network errors, and continues
 * on the SAME session (sessions live in SQLite; the cookie and the actor token stay valid). The agent counts as back
 * when /info answers after a failed poll, or when its startedAt changed (a restart faster than one poll). If the session
 * is really gone afterwards, the GUI goes to the login page with a clear message.
 */
import { goto } from '$app/navigation';
import { base } from '$app/paths';
import { opmFetch, setAuthHold } from '$lib/api';
import { session } from '$lib/state/session.svelte';

export const RESTART_POLL_MS = 2000;
export const RESTART_MAX_MS = 120_000;

export type RestartResult = 'ok' | 'timeout' | 'gone';

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

async function getJson(path: string): Promise<{ status: number; body: unknown } | null> {
  try {
    const res = await opmFetch(path, { method: 'GET', credentials: 'same-origin', cache: 'no-store' });
    let body: unknown = null;
    try {
      body = await res.json();
    } catch {
      /* not json */
    }
    return { status: res.status, body };
  } catch {
    return null; // network error: the agent is not listening (yet)
  }
}

class RestartState {
  active = $state(false);
  title = $state('Restarting the agent…');
  detail = $state('');
  startedMs = $state(0);
  attempts = $state(0);
  sawDown = $state(false);
  timedOut = $state(false);
  lastNote = $state<string | null>(null);
  /** the version /info reported after the restart */
  version = $state<string | null>(null);
  #run = 0;
  /** the agent has no GET /_opm/api/health (before §21 #91) */
  #noHealth = false;
  #pending: { site: string; prevStartedAt: string | null; resolve: (r: RestartResult) => void } | null = null;

  /**
   * Show the overlay and wait until the agent of THIS site answers again (same session).
   * prevStartedAt = AgentSelfInfo.startedAt before the restart (detects a restart that is faster than a poll).
   */
  wait(opts: { title?: string; detail?: string; site: string; prevStartedAt: string | null }): Promise<RestartResult> {
    this.title = opts.title ?? 'Restarting the agent…';
    this.detail = opts.detail ?? '';
    return new Promise<RestartResult>((resolve) => {
      this.#pending?.resolve('timeout');
      this.#pending = { site: opts.site, prevStartedAt: opts.prevStartedAt, resolve };
      this.#loop();
    });
  }
  /** "Keep waiting" after a timeout */
  retry() {
    if (this.#pending) this.#loop();
  }
  /** close after a timeout (the page shows its own error) */
  dismiss() {
    const p = this.#pending;
    this.#pending = null;
    this.active = false;
    this.#run++;
    p?.resolve('timeout');
  }

  async #loop() {
    const my = ++this.#run;
    const p = this.#pending!;
    this.active = true;
    this.timedOut = false;
    this.sawDown = false;
    this.attempts = 0;
    this.lastNote = null;
    this.version = null;
    this.startedMs = Date.now();
    while (my === this.#run) {
      await sleep(RESTART_POLL_MS);
      if (my !== this.#run) return;
      if (Date.now() - this.startedMs > RESTART_MAX_MS) {
        this.timedOut = true;
        return;
      }
      this.attempts++;
      // §21 #91: GET /_opm/api/health (public; 503 while not ok) is the "back" probe; agents without it → /info
      let v: string | null = null;
      let started: string | null = null;
      const h = this.#noHealth ? null : await getJson('/_opm/api/health');
      if (h && h.status === 404) this.#noHealth = true;
      if (!this.#noHealth) {
        const body = h?.body as { ok?: boolean; version?: string; startedAt?: string; checks?: { name: string; ok: boolean; message: string | null }[] } | null;
        if (!h || (h.status >= 500 && !body?.checks)) {
          this.sawDown = true;
          this.lastNote = h ? `the agent answered ${h.status} — still starting` : 'not answering yet — normal while it restarts';
          continue;
        }
        if (!body?.ok) {
          this.sawDown = true;
          const bad = (body?.checks ?? []).filter((c) => !c.ok);
          this.lastNote = `starting · ${bad.map((c) => `${c.name}${c.message ? `: ${c.message}` : ''}`).join(' · ') || 'not healthy yet'}`;
          continue;
        }
        v = body.version ?? null;
        started = body.startedAt ?? null;
        if (!this.sawDown && (!started || started === p.prevStartedAt)) {
          this.lastNote = 'the agent has not stopped yet';
          continue;
        }
      } else {
        const info = await getJson('/_opm/api/info');
        if (!info || info.status >= 500) {
          this.sawDown = true;
          this.lastNote = info ? `the agent answered ${info.status} — still starting` : 'not answering yet — normal while it restarts';
          continue;
        }
        v = (info.body as { agentVersion?: string } | null)?.agentVersion ?? null;
        if (!this.sawDown) {
          // no failed poll yet: did it restart already (startedAt changed)?
          const self = await getJson(`/_opm/api/sites/${encodeURIComponent(p.site)}/agent`);
          if (self?.status === 401) return this.#gone(my);
          started = (self?.body as { startedAt?: string } | null)?.startedAt ?? null;
          if (!self || self.status >= 500 || !started || started === p.prevStartedAt) {
            this.lastNote = 'the agent has not stopped yet';
            continue;
          }
        }
      }
      this.version = v;
      // same session? (the cookie survives the restart; a 401 means the session is really gone)
      const s = await getJson('/_opm/api/session');
      if (!s) {
        this.sawDown = true;
        continue;
      }
      if (s.status === 401) return this.#gone(my);
      if (my !== this.#run) return;
      this.active = false;
      this.#pending = null;
      p.resolve('ok');
      return;
    }
  }
  #gone(my: number) {
    if (my !== this.#run) return;
    const p = this.#pending;
    this.#pending = null;
    this.active = false;
    session.clear('The agent restarted and this login session is no longer valid. Log in again.');
    p?.resolve('gone');
    const here = location.pathname + location.search;
    goto(`${base}/login?next=${encodeURIComponent(here.startsWith(`${base}/app`) ? here : `${base}/app`)}`, { replaceState: true });
  }
}

export const restart = new RestartState();
setAuthHold(() => restart.active);
