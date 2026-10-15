/**
 * Lazy CodeMirror loader (§21 #97) — same approach as lib/tools/xterm.ts: the bundle is built by
 * scripts/vendor-codemirror.mjs into static/assets/vendor/codemirror.js and imported with a non-analysable dynamic
 * import, so it is a separate file (fetched once, only on the dev tools pages) and the main bundle carries only types.
 */
import { base } from '$app/paths';
import type * as CM from './bundle';

export type { EditorHandle, EditorLang, EditorOptions, EditorSchema } from './bundle';
let p: Promise<typeof CM> | null = null;

export function loadCodeMirror(): Promise<typeof CM> {
  if (p) return p;
  p = import(/* @vite-ignore */ `${base}/assets/vendor/codemirror.js`) as Promise<typeof CM>;
  p.catch(() => (p = null));
  return p;
}
