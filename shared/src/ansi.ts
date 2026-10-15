/**
 * ANSI escape handling for log viewers (SPEC-final §21 #93). One small parser shared by the agent GUI and the cloud
 * console: SGR (`ESC [ <params> m`) → styled spans; every other escape (cursor moves, erase, OSC titles / hyperlinks,
 * charset selects) is removed, keeping the visible text. No dependency.
 *
 * ESC lost: some pipelines strip the 0x1b byte and leave "[0m[1m[31merror[0m". `parseAnsi(…, { escLost: true })` then
 * treats a bare `[<digits;…>m` as SGR. It is never guessed per line: callers decide per stream with `detectEscLost()`
 * (no ESC anywhere AND a bare reset `[0m` / `[m` at a line start, after a space or next to another bare SGR). A legitimate
 * "[1]" or "[x]" is never touched (no final `m`), and with ESC present a bare "[1m" stays text.
 */

/** A color: a palette index 0–255 (0–15 = the 16 theme colors) or a truecolor hex `#rrggbb`. */
export type AnsiColor = { kind: 'idx'; n: number } | { kind: 'rgb'; hex: string };
export interface AnsiStyle {
  fg?: AnsiColor;
  bg?: AnsiColor;
  bold?: true;
  dim?: true;
  italic?: true;
  underline?: true;
  inverse?: true;
  strike?: true;
}
export interface AnsiSpan {
  text: string;
  style: AnsiStyle;
}
export interface AnsiOptions {
  /** the stream lost its ESC bytes: bare `[<params>m` are SGR (see detectEscLost) */
  escLost?: boolean;
}

const ESC = '\x1b';
// CSI: ESC [ params intermediates final; OSC: ESC ] … (BEL | ESC \); other two/three-byte escapes
const ANY_ESC = /\x1b(?:\[([0-?]*)([ -/]*)([@-~])|\][^\x07\x1b]*(?:\x07|\x1b\\)?|[PX^_][^\x1b]*(?:\x1b\\)?|[()*+][0-9A-Za-z]|[ -/]*[0-~])?/g;
const BARE_SGR = /\[((?:\d{1,3}(?:[;:]\d{1,3})*)?)m/g;
// other C0 controls (not \t, \n): bell, backspace, \r … are removed for display
const C0 = /[\x00-\x08\x0b-\x1a\x1c-\x1f\x7f]/g;

/** true when the text holds an ESC byte. */
export const hasEsc = (s: string) => s.includes(ESC);

/**
 * Decide for a whole stream whether ESC bytes were lost: no ESC anywhere and at least one bare reset (`[0m` or `[m`) at a
 * line start, after whitespace or directly next to another bare SGR (`[0m[1m`, `[31merror[0m`).
 */
export function detectEscLost(lines: readonly string[]): boolean {
  let bare = false;
  for (const l of lines) {
    if (l.includes(ESC)) return false;
    if (!bare && /(?:^|\s)\[0m|\[[\d;]*m\[[\d;]*m/.test(l)) bare = true;
  }
  return bare;
}

function color256(params: number[], i: number): { c: AnsiColor | undefined; used: number } {
  const mode = params[i + 1];
  if (mode === 5 && params[i + 2] !== undefined) {
    const n = params[i + 2]!;
    return { c: n >= 0 && n <= 255 ? { kind: 'idx', n } : undefined, used: 2 };
  }
  if (mode === 2 && params[i + 4] !== undefined) {
    const [r, g, b] = [params[i + 2]!, params[i + 3]!, params[i + 4]!].map((x) => Math.max(0, Math.min(255, x)));
    const hex = '#' + [r, g, b].map((x) => x!.toString(16).padStart(2, '0')).join('');
    return { c: { kind: 'rgb', hex }, used: 4 };
  }
  return { c: undefined, used: mode === 5 ? 2 : mode === 2 ? 4 : 1 };
}

/** Apply one SGR parameter list to a style (returns a new style object). */
export function applySgr(style: AnsiStyle, paramText: string): AnsiStyle {
  const s: AnsiStyle = { ...style };
  const params = paramText === '' ? [0] : paramText.split(/[;:]/).map((p) => (p === '' ? 0 : Number(p)));
  for (let i = 0; i < params.length; i++) {
    const p = params[i]!;
    if (p === 0) for (const k of Object.keys(s)) delete (s as Record<string, unknown>)[k];
    else if (p === 1) s.bold = true;
    else if (p === 2) s.dim = true;
    else if (p === 3) s.italic = true;
    else if (p === 4) s.underline = true;
    else if (p === 7) s.inverse = true;
    else if (p === 9) s.strike = true;
    else if (p === 21 || p === 22) {
      delete s.bold;
      delete s.dim;
    } else if (p === 23) delete s.italic;
    else if (p === 24) delete s.underline;
    else if (p === 27) delete s.inverse;
    else if (p === 29) delete s.strike;
    else if (p >= 30 && p <= 37) s.fg = { kind: 'idx', n: p - 30 };
    else if (p === 38) {
      const { c, used } = color256(params, i);
      if (c) s.fg = c;
      i += used;
    } else if (p === 39) delete s.fg;
    else if (p >= 40 && p <= 47) s.bg = { kind: 'idx', n: p - 40 };
    else if (p === 48) {
      const { c, used } = color256(params, i);
      if (c) s.bg = c;
      i += used;
    } else if (p === 49) delete s.bg;
    else if (p >= 90 && p <= 97) s.fg = { kind: 'idx', n: p - 90 + 8 };
    else if (p >= 100 && p <= 107) s.bg = { kind: 'idx', n: p - 100 + 8 };
    // anything else (blink, fonts, overline …) is ignored
  }
  return s;
}

const sameStyle = (a: AnsiStyle, b: AnsiStyle) => JSON.stringify(a) === JSON.stringify(b);

/**
 * Parse one line (or a chunk) into spans. `state` = the style carried over from the previous line (SGR state survives a
 * newline, as in a terminal); the returned `state` is the style at the end. Non-SGR escapes are dropped.
 */
export function parseAnsi(text: string, state: AnsiStyle = {}, opts: AnsiOptions = {}): { spans: AnsiSpan[]; state: AnsiStyle } {
  const spans: AnsiSpan[] = [];
  let style = state;
  const push = (t: string) => {
    t = t.replace(C0, '');
    if (!t) return;
    const last = spans[spans.length - 1];
    if (last && sameStyle(last.style, style)) last.text += t;
    else spans.push({ text: t, style });
  };
  const sgrIn = (chunk: string) => {
    if (!opts.escLost) return push(chunk);
    let at = 0;
    for (const m of chunk.matchAll(BARE_SGR)) {
      push(chunk.slice(at, m.index));
      style = applySgr(style, m[1] ?? '');
      at = m.index! + m[0].length;
    }
    push(chunk.slice(at));
  };
  let at = 0;
  for (const m of text.matchAll(ANY_ESC)) {
    sgrIn(text.slice(at, m.index));
    if (m[3] === 'm' && m[2] === '' && /^[\d;:]*$/.test(m[1] ?? '')) style = applySgr(style, m[1] ?? '');
    at = m.index! + m[0].length;
  }
  sgrIn(text.slice(at));
  return { spans, state: style };
}

/** The visible text: every escape removed (SGR too; bare SGR as well when escLost). Use it for search and copy. */
export function stripAnsi(text: string, opts: AnsiOptions = {}): string {
  let out = text.replace(ANY_ESC, '');
  if (opts.escLost) out = out.replace(BARE_SGR, '');
  return out.replace(C0, '');
}

/** The xterm 256-color palette entry as #rrggbb (16–255; 0–15 are theme colors: use CSS variables --ansi-0 … --ansi-15). */
export function ansi256Hex(n: number): string {
  const h = (x: number) => x.toString(16).padStart(2, '0');
  if (n >= 232) {
    const v = 8 + (n - 232) * 10;
    return `#${h(v)}${h(v)}${h(v)}`;
  }
  if (n >= 16) {
    const i = n - 16;
    const lv = (x: number) => (x === 0 ? 0 : 55 + x * 40);
    return `#${h(lv(Math.floor(i / 36)))}${h(lv(Math.floor(i / 6) % 6))}${h(lv(i % 6))}`;
  }
  return ANSI16_FALLBACK[n] ?? '#808080';
}
/** Fallback hex for the 16 base colors (the GUIs use CSS variables from their design tokens instead). */
export const ANSI16_FALLBACK = ['#1f2328', '#cf222e', '#116329', '#9a6700', '#0550ae', '#8250df', '#1b7c83', '#6e7781', '#57606a', '#a40e26', '#1a7f37', '#7d4e00', '#0969da', '#a475f9', '#3192aa', '#8c959f'];

/** CSS color for a palette / truecolor color: 0–15 → `var(--ansi-N, fallback)`, 16–255 + truecolor → hex. */
export function ansiCssColor(c: AnsiColor): string {
  if (c.kind === 'rgb') return c.hex;
  if (c.n < 16) return `var(--ansi-${c.n}, ${ANSI16_FALLBACK[c.n]})`;
  return ansi256Hex(c.n);
}

/** Inline CSS for a style. */
export function ansiCss(style: AnsiStyle): string {
  const fg = style.fg;
  const bg = style.bg;
  const out: string[] = [];
  if (style.inverse) {
    out.push(`color:${bg ? ansiCssColor(bg) : 'var(--ansi-bg, #fff)'}`, `background:${fg ? ansiCssColor(fg) : 'var(--ansi-fg, currentColor)'}`);
  } else {
    if (fg) out.push(`color:${ansiCssColor(fg)}`);
    if (bg) out.push(`background:${ansiCssColor(bg)}`);
  }
  if (style.bold) out.push('font-weight:600');
  if (style.dim && !style.fg) out.push('opacity:.8'); // dim with its own color (Deno's gray 245 stack lines) keeps the color: readable
  if (style.italic) out.push('font-style:italic');
  const deco = [style.underline ? 'underline' : '', style.strike ? 'line-through' : ''].filter(Boolean).join(' ');
  if (deco) out.push(`text-decoration:${deco}`);
  return out.join(';');
}

/** Escape text for innerHTML. */
const escHtml = (s: string) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');

/** Spans → safe HTML (text escaped; style only from the parser — never from the input). */
export function spansToHtml(spans: readonly AnsiSpan[]): string {
  return spans.map((s) => {
    const css = ansiCss(s.style);
    return css ? `<span style="${css}">${escHtml(s.text)}</span>` : escHtml(s.text);
  }).join('');
}

/**
 * Render a list of lines to HTML strings (one per line), carrying the SGR state across lines.
 * `colors: false` = plain text (escapes removed); `raw: true` = the bytes as text with ESC shown as "␛".
 */
export function renderAnsiLines(lines: readonly string[], o: { colors?: boolean; raw?: boolean; escLost?: boolean } = {}): string[] {
  if (o.raw) return lines.map((l) => escHtml(l.replace(/\x1b/g, '␛')));
  if (o.colors === false) return lines.map((l) => escHtml(stripAnsi(l, { escLost: o.escLost })));
  let state: AnsiStyle = {};
  return lines.map((l) => {
    const r = parseAnsi(l, state, { escLost: o.escLost });
    state = r.state;
    return spansToHtml(r.spans);
  });
}
