/**
 * ANSI log rendering for every log view of the agent GUI (SPEC-final §21 #93), on top of `@opm/shared/ansi`.
 *
 * - `ansiPrefs`: the two global toggles (Colors on by default, Raw off) — one setting for every log view, remembered per
 *   browser (localStorage `opm.logs.colors` / `opm.logs.raw`, wrapped in try/catch).
 * - `AnsiStreams`: incremental parser for live streams. SGR state is carried across the lines of ONE stream (key = e.g.
 *   container + stdout/stderr); ESC-lost detection is per stream (the shared `detectEscLost` rule) — when a stream flips,
 *   only its own lines are parsed again and `version` changes so the view re-renders.
 * - `parseLines`: the same for a static list of lines (one stream).
 * - `lineHtml`: one line → safe HTML (text escaped, styles only from the parser) for the current mode, with the search
 *   term highlighted on the VISIBLE text: the <mark> is placed inside the styled spans, so a match never breaks a span.
 */
import { detectEscLost, hasEsc, parseAnsi, ansiCss, type AnsiSpan, type AnsiStyle } from '@opm/shared/ansi';

const read = (k: string, d: boolean) => {
  try {
    const v = localStorage.getItem(k);
    return v === null ? d : v === '1';
  } catch {
    return d;
  }
};
const write = (k: string, v: boolean) => {
  try {
    localStorage.setItem(k, v ? '1' : '0');
  } catch {
    /* private window / blocked storage: the toggle still works for this page */
  }
};

class AnsiPrefs {
  #colors = $state(typeof localStorage === 'undefined' ? true : read('opm.logs.colors', true));
  #raw = $state(typeof localStorage === 'undefined' ? false : read('opm.logs.raw', false));
  get colors() {
    return this.#colors;
  }
  set colors(v: boolean) {
    this.#colors = v;
    write('opm.logs.colors', v);
  }
  get raw() {
    return this.#raw;
  }
  set raw(v: boolean) {
    this.#raw = v;
    write('opm.logs.raw', v);
  }
  /** 'raw' wins over colors */
  get mode(): AnsiMode {
    return this.#raw ? 'raw' : this.#colors ? 'colors' : 'plain';
  }
}
export type AnsiMode = 'colors' | 'plain' | 'raw';
export const ansiPrefs = new AnsiPrefs();

/** One parsed line: spans (styled), plain = the visible text (search / copy), raw = the bytes as received. */
export interface ParsedLine {
  spans: AnsiSpan[];
  plain: string;
  raw: string;
}

const plainOf = (spans: AnsiSpan[]) => {
  let s = '';
  for (const x of spans) s += x.text;
  return s;
};
/** a bare-SGR mark of one line (no ESC in it): the per-line half of detectEscLost */
const bareMark = (l: string) => !hasEsc(l) && detectEscLost([l]);

/** Parse a static list of lines (one stream): ESC-lost decided for the whole list, SGR state carried across lines. */
export function parseLines(lines: readonly string[]): ParsedLine[] {
  const escLost = detectEscLost(lines);
  let state: AnsiStyle = {};
  return lines.map((raw) => {
    const r = parseAnsi(raw, state, { escLost });
    state = r.state;
    return { spans: r.spans, plain: plainOf(r.spans), raw };
  });
}

/** Parse a static list of interleaved streams (e.g. stdout + stderr): state + ESC-lost detection per `key`. Pure. */
export function parseKeyed(lines: readonly { key: string; raw: string }[]): ParsedLine[] {
  const byKey = new Map<string, string[]>();
  for (const l of lines) {
    const a = byKey.get(l.key);
    if (a) a.push(l.raw);
    else byKey.set(l.key, [l.raw]);
  }
  const lost = new Map([...byKey].map(([k, v]) => [k, detectEscLost(v)]));
  const state = new Map<string, AnsiStyle>();
  return lines.map((l) => {
    const r = parseAnsi(l.raw, state.get(l.key) ?? {}, { escLost: lost.get(l.key) });
    state.set(l.key, r.state);
    return { spans: r.spans, plain: plainOf(r.spans), raw: l.raw };
  });
}

interface Stream {
  ids: number[];
  state: AnsiStyle;
  esc: boolean;
  bare: boolean;
  lost: boolean;
}

/** Incremental per-stream parser for live log views (ids must grow; `prune` drops lines that left the view). */
export class AnsiStreams {
  #streams = new Map<string, Stream>();
  #lines = new Map<number, ParsedLine>();
  #raw = new Map<number, string>();
  /** changes when lines already shown were parsed again (a stream turned out to be ESC-lost, or got an ESC) */
  version = $state(0);

  add(id: number, key: string, raw: string): ParsedLine {
    let s = this.#streams.get(key);
    if (!s) this.#streams.set(key, (s = { ids: [], state: {}, esc: false, bare: false, lost: false }));
    if (!s.esc && hasEsc(raw)) s.esc = true;
    if (!s.bare && bareMark(raw)) s.bare = true;
    s.ids.push(id);
    this.#raw.set(id, raw);
    const lost = !s.esc && s.bare;
    if (lost !== s.lost) {
      // the decision changed for this stream: parse its lines again from the start (rare: once per stream at most twice)
      s.lost = lost;
      s.state = {};
      for (const x of s.ids) {
        const rx = this.#raw.get(x);
        if (rx === undefined) continue;
        const r = parseAnsi(rx, s.state, { escLost: lost });
        s.state = r.state;
        this.#lines.set(x, { spans: r.spans, plain: plainOf(r.spans), raw: rx });
      }
      if (s.ids.length > 1) this.version++;
      return this.#lines.get(id)!;
    }
    const r = parseAnsi(raw, s.state, { escLost: lost });
    s.state = r.state;
    const p = { spans: r.spans, plain: plainOf(r.spans), raw };
    this.#lines.set(id, p);
    return p;
  }
  /** reads `version`, so a view re-renders when a stream was parsed again */
  get(id: number): ParsedLine | undefined {
    void this.version;
    return this.#lines.get(id);
  }
  /** the stream was detected as ESC-lost */
  escLost(key: string): boolean {
    return !!this.#streams.get(key)?.lost;
  }
  /** forget lines with id < minId (the view keeps the last N lines) */
  prune(minId: number) {
    for (const s of this.#streams.values()) {
      let k = 0;
      while (k < s.ids.length && s.ids[k] < minId) {
        this.#lines.delete(s.ids[k]);
        this.#raw.delete(s.ids[k]);
        k++;
      }
      if (k) s.ids.splice(0, k);
    }
  }
  reset() {
    this.#streams.clear();
    this.#lines.clear();
    this.#raw.clear();
  }
}

const escHtml = (s: string) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');

/**
 * CSS of a style. Dim: the shared parser uses opacity .72; on the dark log background that makes an explicit gray (e.g.
 * Deno's dim 256-color 245 "at …" lines) too faint, so a dim span WITH an explicit color keeps its color (the color is
 * already the dimmed shade) and a dim span without one uses opacity .8.
 */
export function styleCss(st: AnsiStyle): string {
  const css = ansiCss(st);
  if (!st.dim) return css;
  return st.fg && !st.inverse ? css.replace(/;?opacity:\.72/, '') : css.replace('opacity:.72', 'opacity:.8');
}

/** case-insensitive match ranges of q in text */
function ranges(text: string, q: string): [number, number][] {
  const out: [number, number][] = [];
  if (!q) return out;
  const lo = text.toLowerCase();
  const ql = q.toLowerCase();
  let i = 0;
  for (;;) {
    const j = lo.indexOf(ql, i);
    if (j < 0) break;
    out.push([j, j + ql.length]);
    i = j + Math.max(1, ql.length);
  }
  return out;
}
/** escaped text of [a, b) with the parts inside `rs` wrapped in <mark> */
function marked(text: string, offset: number, rs: [number, number][]): string {
  if (!rs.length) return escHtml(text);
  let out = '';
  let at = 0;
  const end = text.length;
  for (const [s0, e0] of rs) {
    const s = Math.max(0, s0 - offset);
    const e = Math.min(end, e0 - offset);
    if (e <= 0 || s >= end || e <= s) continue;
    if (s > at) out += escHtml(text.slice(at, s));
    out += `<mark>${escHtml(text.slice(Math.max(s, at), e))}</mark>`;
    at = e;
  }
  if (at < end) out += escHtml(text.slice(at));
  return out;
}

/** True when the line's visible text contains q (case-insensitive); use for counts / filters. */
export const lineMatches = (p: ParsedLine, q: string) => !!q && p.plain.toLowerCase().includes(q.toLowerCase());

/** One parsed line → HTML for a mode; `q` = search term to highlight (on the visible text in colors / plain mode). */
export function lineHtml(p: ParsedLine, mode: AnsiMode, q = ''): string {
  if (mode === 'raw') {
    const t = p.raw.replace(/\x1b/g, '␛');
    return marked(t, 0, ranges(t, q));
  }
  if (mode === 'plain') return marked(p.plain, 0, ranges(p.plain, q));
  const rs = ranges(p.plain, q);
  let out = '';
  let pos = 0;
  for (const s of p.spans) {
    const css = styleCss(s.style);
    const inner = marked(s.text, pos, rs);
    out += css ? `<span style="${css}">${inner}</span>` : inner;
    pos += s.text.length;
  }
  return out;
}

/** Single line without carried state (agent / debug messages): parsed on the fly. */
export function textHtml(raw: string, mode: AnsiMode, q = ''): string {
  if (mode !== 'raw' && !hasEsc(raw) && !bareMark(raw)) return marked(raw, 0, ranges(raw, q));
  return lineHtml(parseLines([raw])[0], mode, q);
}

/** "<ISO ts> <stream> <text>" (ContainerFailure.logTail, update.failed data) → ["HH:MM:SS stream ", text] */
export function logTailPrefix(l: string): [string, string] {
  const m = /^\d{4}-\d\d-\d\dT(\d\d:\d\d:\d\d)\.?\d*Z?\s(?:(stdout|stderr)\s)?/.exec(l);
  return m ? [`${m[1]} ${m[2] ? m[2] + ' ' : ''}`, l.slice(m[0].length)] : ['', l];
}
