/**
 * Login session + actor (SPEC §4.6). The actor token is held in page memory ONLY: every page load / refresh /
 * new tab shows the "Who is working right now?" popup again.
 */
import type { AgentInfoResponse, SessionResponse } from '@opm/shared/types';

const RECENT_KEY = 'opm.recentActors';

export interface ActorState {
  email: string;
  token: string;
  expiresAt: string;
}

class SessionStore {
  info = $state<AgentInfoResponse | null>(null);
  session = $state<SessionResponse | null>(null);
  actor = $state<ActorState | null>(null);
  /** set when a 401 kicked us out (shown on the login page) */
  endedReason = $state<string | null>(null);
  /** true while the actor popup must be shown (session ok, no actor) */
  askActor = $state(false);
  /** local clock + skew = agent clock */
  skewMs = $state(0);
  /** ticks every second (components derive countdowns from it) */
  tick = $state(Date.now());

  get actorToken(): string | null {
    return this.actor?.token ?? null;
  }
  now(): number {
    return this.tick + this.skewMs;
  }
  setServerTime(ts: string | null | undefined) {
    if (!ts) return;
    const t = Date.parse(ts);
    if (Number.isFinite(t)) this.skewMs = t - Date.now();
  }
  /** milliseconds until the session expires (agent clock) */
  sessionLeftMs(): number {
    if (!this.session) return 0;
    return Date.parse(this.session.expiresAt) - this.now();
  }
  clear(reason: string | null) {
    this.session = null;
    this.actor = null;
    this.askActor = false;
    this.endedReason = reason;
  }
}

export const session = new SessionStore();

if (typeof window !== 'undefined') {
  setInterval(() => (session.tick = Date.now()), 1000);
}

export function recentLocalActors(): string[] {
  try {
    const v = JSON.parse(localStorage.getItem(RECENT_KEY) ?? '[]');
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string').slice(0, 10) : [];
  } catch {
    return [];
  }
}
export function rememberActor(email: string) {
  const list = [email, ...recentLocalActors().filter((e) => e !== email)].slice(0, 10);
  try {
    localStorage.setItem(RECENT_KEY, JSON.stringify(list));
  } catch {
    /* storage blocked */
  }
}
export function forgetActor(email: string) {
  try {
    localStorage.setItem(RECENT_KEY, JSON.stringify(recentLocalActors().filter((e) => e !== email)));
  } catch {
    /* storage blocked */
  }
}
