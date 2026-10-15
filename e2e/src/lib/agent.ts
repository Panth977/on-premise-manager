// Agent local API as an operator: access-key login (cookie jar) + actor token.
import { createAgentClient, type AgentClient } from '../../../shared/dist/agent-api.js';
import { log } from './report.js';

export class AgentSession {
  actorToken: string | null = null;
  readonly api: AgentClient;
  constructor(public readonly base: string, public readonly label: string) {
    this.api = createAgentClient(base, () => this.actorToken, { fetch: this.fetch });
  }
  /** session cookies: `opm_session_<siteSlug>` (preferred, SPEC §4.6) and the legacy `opm_session` */
  jar = new Map<string, string>();
  get cookie(): string | null {
    return this.jar.size ? [...this.jar].map(([k, v]) => `${k}=${v}`).join('; ') : null;
  }
  set cookie(c: string | null) {
    this.jar.clear();
    for (const part of (c ?? '').split(';')) {
      const [k, v] = part.trim().split('=');
      if (k && v !== undefined) this.jar.set(k, v);
    }
  }
  fetch = async (input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> => {
    const headers = new Headers(init.headers);
    const c = this.cookie;
    if (c) headers.set('Cookie', c);
    const r = await fetch(input, { ...init, headers });
    for (const sc of r.headers.getSetCookie()) {
      const [kv, ...attrs] = sc.split(';');
      const i = kv.indexOf('=');
      const k = kv.slice(0, i).trim();
      const v = kv.slice(i + 1).trim();
      if (k !== 'opm_session' && !k.startsWith('opm_session_')) continue;
      const maxAge = attrs.find((a) => a.trim().toLowerCase().startsWith('max-age='));
      if ((maxAge && maxAge.trim().slice(8) === '0') || v === '') this.jar.delete(k);
      else this.jar.set(k, v);
    }
    return r;
  };
  async login(accessKey: string, actor: string): Promise<void> {
    const r = await this.api.login({ body: { accessKey } });
    if (!this.cookie) throw new Error(`${this.label}: login ok (session ${r.sessionId}) but no opm_session cookie was set`);
    const a = await this.api.declareActor({ body: { email: actor } });
    this.actorToken = a.actorToken;
    log(`${this.label}: logged in, session ${r.sessionId}, actor ${a.email}`);
  }
  clone(label: string): AgentSession {
    const s = new AgentSession(this.base, label);
    s.cookie = this.cookie;
    s.actorToken = this.actorToken;
    return s;
  }
}
