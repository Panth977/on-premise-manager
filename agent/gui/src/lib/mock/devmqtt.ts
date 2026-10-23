/**
 * Mock MQTT broker of the dev tools (§21 #97): retained messages, live sessions with topic filters (+ / #), a fake
 * EventSource for the ticketed session stream (meta → status → retained → a live message every 350 ms, one `dropped`
 * event after 40 messages), publish delivered to every matching session, topics seen. Knob: opm.mock.mqttEndAfter=<n>
 * ends every stream after n messages with an unexpected `end` (reconnect test).
 */
import type { MqttMessage, MqttSubscription, MqttTopicSeen } from '@opm/shared/devtools';
import { setDevStreamImpl } from '$lib/devtools/api';

interface Sess {
  id: string;
  connId: string;
  site: string;
  subs: MqttSubscription[];
  ticket: string | null;
  es: MockMqttEventSource | null;
  closed: boolean;
  messages: number;
}
const sessions = new Map<string, Sess>();
const retained = new Map<string, MqttMessage>();
const seen = new Map<string, MqttTopicSeen>();
let seq = 0;
const now = () => Date.now();
const knob = (k: string) => {
  try {
    return localStorage.getItem(k);
  } catch {
    return null;
  }
};

export function topicMatches(filter: string, topic: string): boolean {
  const f = filter.split('/');
  const t = topic.split('/');
  if (topic.startsWith('$') && !filter.startsWith('$')) return false;
  for (let i = 0; i < f.length; i++) {
    if (f[i] === '#') return true;
    if (i >= t.length) return false;
    if (f[i] !== '+' && f[i] !== t[i]) return false;
  }
  return f.length === t.length;
}
function b64(s: Uint8Array) {
  let bin = '';
  for (const b of s) bin += String.fromCharCode(b);
  return btoa(bin);
}
export function makeMessage(topic: string, bytes: Uint8Array, qos: number, retain: boolean): MqttMessage {
  let text: string | null = null;
  try {
    text = new TextDecoder('utf-8', { fatal: true }).decode(bytes);
  } catch {
    text = null;
  }
  return { seq: ++seq, at: new Date().toISOString(), topic, qos, retained: retain, duplicate: false, sizeBytes: bytes.length, text, base64: text === null ? b64(bytes) : null, truncated: false };
}
function note(m: MqttMessage) {
  const s = seen.get(m.topic);
  seen.set(m.topic, { topic: m.topic, messages: (s?.messages ?? 0) + 1, lastAt: m.at, retained: m.retained || (s?.retained ?? false), lastPreview: m.text !== null ? m.text.slice(0, 200) : `(${m.sizeBytes} bytes binary)`, lastSizeBytes: m.sizeBytes });
}
const enc = (s: string) => new TextEncoder().encode(s);
const DEVICES = ['SENSOR-1', 'SENSOR-2', 'SENSOR-3', 'SENSOR-4'];
(function seed() {
  for (const d of DEVICES) {
    const m = makeMessage(`northwind/devices/${d}/status`, enc(JSON.stringify({ online: d !== 'SENSOR-4', fw: '4.2.1', rssi: -61 })), 1, true);
    retained.set(m.topic, m);
    note(m);
  }
  const cfg = makeMessage('northwind/config/thresholds', enc(JSON.stringify({ pm25: 60, pm10: 100, noise: 75 })), 1, true);
  retained.set(cfg.topic, cfg);
  note(cfg);
  const bin = makeMessage('northwind/devices/SENSOR-1/raw', new Uint8Array([0x01, 0x02, 0xfe, 0xff, 0x10, 0x80, 0x00, 0x7f]), 0, true);
  retained.set(bin.topic, bin);
  note(bin);
})();

let tick = 0;
function liveMessage(): MqttMessage {
  tick++;
  const d = DEVICES[tick % DEVICES.length]!;
  if (tick % 9 === 0) return makeMessage(`northwind/alerts/${d}`, enc(JSON.stringify({ level: tick % 18 === 0 ? 'critical' : 'warning', param: 'pm25', value: 80 + (tick % 40), at: new Date().toISOString() })), 1, false);
  if (tick % 13 === 0) return makeMessage('$SYS/broker/clients/connected', enc(String(5 + (tick % 4))), 0, false);
  if (tick % 5 === 0) return makeMessage(`sensors/${d.toLowerCase()}/noise`, enc(String(55 + (tick % 20))), 0, false);
  return makeMessage(`northwind/devices/${d}/data`, enc(JSON.stringify({ ts: new Date().toISOString(), pm25: Math.round((30 + Math.sin(tick / 3) * 20) * 10) / 10, pm10: 70 + (tick % 30), temp: 28.4, hum: 61 })), 0, false);
}

/** deliver one message to every session whose filters match */
export function deliver(m: MqttMessage) {
  note(m);
  for (const s of sessions.values()) if (!s.closed && s.es && s.subs.some((f) => topicMatches(f.filter, m.topic))) s.es.push(m);
}
export function publish(topic: string, bytes: Uint8Array, qos: number, retain: boolean): MqttMessage {
  const m = makeMessage(topic, bytes, qos, false);
  if (retain) {
    if (bytes.length === 0) retained.delete(topic);
    else retained.set(topic, { ...m, retained: true });
    const s = seen.get(topic);
    if (s && bytes.length === 0) s.retained = false;
  }
  deliver(m);
  return m;
}
export function openSession(site: string, connId: string, subs: MqttSubscription[]) {
  // one live session per connection: a new Start replaces the earlier one (as the agent does)
  for (const x of sessions.values()) if (!x.closed && x.site === site && x.connId === connId) closeSession(x.id, 'replaced by a new session');
  const live = [...sessions.values()].filter((s) => !s.closed);
  if (live.length >= 4) return null;
  const id = `ms_${(++seq).toString(36)}${Math.random().toString(36).slice(2, 8)}`;
  const s: Sess = { id, connId, site, subs: [...subs], ticket: `tk_${Math.random().toString(36).slice(2)}`, es: null, closed: false, messages: 0 };
  sessions.set(id, s);
  return s;
}
export const sessionOf = (id: string) => sessions.get(id) ?? null;
export function closeSession(id: string, reason = 'closed by the operator') {
  const s = sessions.get(id);
  if (!s) return false;
  s.closed = true;
  s.es?.end(reason);
  return true;
}
export function retainedFor(filter: string) {
  return [...retained.values()].filter((m) => topicMatches(filter, m.topic));
}
export function topicsSeen(): MqttTopicSeen[] {
  return [...seen.values()].sort((a, b) => b.lastAt.localeCompare(a.lastAt)).slice(0, 1000);
}

class MockMqttEventSource {
  readyState = 0;
  onerror: ((e: Event) => void) | null = null;
  #l = new Map<string, ((e: MessageEvent) => void)[]>();
  #iv: ReturnType<typeof setInterval> | null = null;
  #t: ReturnType<typeof setTimeout> | null = null;
  #s: Sess | null = null;
  #n = 0;
  constructor(url: string) {
    const u = new URL(url, location.origin);
    const m = /\/mqtt\/sessions\/([^/]+)\/stream$/.exec(u.pathname);
    const s = m ? sessions.get(decodeURIComponent(m[1]!)) : undefined;
    const ticket = u.searchParams.get('ticket');
    this.#t = setTimeout(() => {
      if (!s || s.closed || !ticket || s.ticket !== ticket) {
        this.readyState = 2;
        this.onerror?.(new Event('error'));
        return;
      }
      s.ticket = null; // single use
      s.es = this;
      this.#s = s;
      this.readyState = 1;
      this.#emit('open', {});
      this.#emit('meta', { sessionId: s.id, subscriptions: s.subs });
      this.#emit('status', { connected: true, error: null });
      const sent = new Set<string>();
      for (const f of s.subs)
        for (const r of retainedFor(f.filter))
          if (!sent.has(r.topic)) {
            sent.add(r.topic);
            this.push({ ...r, seq: ++seq, at: new Date().toISOString(), retained: true });
          }
      const endAfter = Number(knob('opm.mock.mqttEndAfter') ?? 0);
      this.#iv = setInterval(() => {
        const msg = liveMessage();
        deliver(msg);
        this.#n++;
        if (this.#n === 40) this.#emit('dropped', { count: 7 });
        if (endAfter && this.#n >= endAfter) this.end('broker connection lost', true);
      }, 350);
    }, 60);
  }
  push(m: MqttMessage) {
    if (!this.#s) return;
    this.#s.messages++;
    this.#emit('message', m);
  }
  end(reason: string, unexpected = false) {
    if (this.readyState === 2) return;
    this.#emit(unexpected ? 'error' : 'end', unexpected ? { message: reason } : { reason });
    this.close();
  }
  #emit(type: string, data: unknown) {
    const ev = new MessageEvent(type, { data: JSON.stringify(data) });
    for (const f of this.#l.get(type) ?? []) f(ev);
  }
  addEventListener(type: string, fn: (e: MessageEvent) => void) {
    this.#l.set(type, [...(this.#l.get(type) ?? []), fn]);
  }
  close() {
    this.readyState = 2;
    if (this.#iv) clearInterval(this.#iv);
    if (this.#t) clearTimeout(this.#t);
    if (this.#s) {
      this.#s.closed = true;
      this.#s.es = null;
    }
  }
}
setDevStreamImpl(MockMqttEventSource as never);
export const _now = now;
