/** `<auto:slug>` placeholders (shared autoHostPlaceholder) in resolved values → text + chip segments. */
import { AUTO_HOST_RE } from '@opm/shared/expr';

export type AutoSeg = { text: string; auto: null } | { text: string; auto: string };
export function autoSegments(text: string): AutoSeg[] {
  const out: AutoSeg[] = [];
  const re = new RegExp(AUTO_HOST_RE.source, 'g');
  let last = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(text))) {
    if (m.index > last) out.push({ text: text.slice(last, m.index), auto: null });
    out.push({ text: m[0], auto: m[1]! });
    last = m.index + m[0].length;
  }
  if (last < text.length || !out.length) out.push({ text: text.slice(last), auto: null });
  return out;
}
export function hasAutoHost(text: string | null | undefined): boolean {
  return !!text && new RegExp(AUTO_HOST_RE.source).test(text);
}
