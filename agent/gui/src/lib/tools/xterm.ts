/**
 * Lazy xterm.js loader (round T). xterm + the fit addon are vendored into static/assets/vendor/ by
 * scripts/vendor-xterm.mjs (embedded in the agent binary, no CDN) and imported only by the Terminal page, so the
 * single main bundle does not carry ~340 KB of terminal code.
 */
import { base } from '$app/paths';
import type { Terminal as XTerminal, ITerminalOptions } from '@xterm/xterm';
import type { FitAddon as XFitAddon } from '@xterm/addon-fit';

export type { XTerminal, XFitAddon };
interface XtermModules {
  Terminal: new (o?: ITerminalOptions) => XTerminal;
  FitAddon: new () => XFitAddon;
}
let p: Promise<XtermModules> | null = null;

export function loadXterm(): Promise<XtermModules> {
  if (p) return p;
  const href = `${base}/assets/vendor/xterm.css`;
  if (!document.querySelector(`link[href="${href}"]`)) {
    const l = document.createElement('link');
    l.rel = 'stylesheet';
    l.href = href;
    document.head.appendChild(l);
  }
  p = Promise.all([import(/* @vite-ignore */ `${base}/assets/vendor/xterm.js`), import(/* @vite-ignore */ `${base}/assets/vendor/xterm-addon-fit.js`)]).then(([a, b]) => ({ Terminal: a.Terminal, FitAddon: b.FitAddon }));
  p.catch(() => (p = null));
  return p;
}

export const TERM_THEME = {
  background: '#121216',
  foreground: '#e4e4e7',
  cursor: '#a5b4fc',
  cursorAccent: '#121216',
  selectionBackground: 'rgba(129, 140, 248, 0.35)',
  black: '#18181b',
  red: '#f87171',
  green: '#4ade80',
  yellow: '#fbbf24',
  blue: '#60a5fa',
  magenta: '#c084fc',
  cyan: '#22d3ee',
  white: '#e4e4e7',
  brightBlack: '#71717a',
  brightRed: '#fca5a5',
  brightGreen: '#86efac',
  brightYellow: '#fde68a',
  brightBlue: '#93c5fd',
  brightMagenta: '#d8b4fe',
  brightCyan: '#67e8f9',
  brightWhite: '#fafafa',
};
