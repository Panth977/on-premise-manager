/**
 * Icons library (icons/{iconId}): type sniffing, SVG sanitizing, usage scan, icon-field validation (format + existing
 * upload) and data-URL embedding for revision catalogs.
 *
 * Storage: the bytes live IN the Firestore doc as the internal field `_bytes` (Firestore Bytes, ≤ 64 KiB), so an icon
 * is one document (atomic create / delete, one read to serve). Internal fields are stripped from API answers.
 */
import { checkIcon, checkPicture, iconUploadId, ICON_CONTENT_TYPES, Issues, type IconContentType, type IconDoc, type IconUser } from '@opm/shared';
import { col, db } from './fire.js';
import { decodeTemplate } from './codec.js';
import type { V } from './util.js';

// ───────────────────────────────────────── sniffing
/** Content type from the magic bytes (png, webp, svg text), or null. */
export function sniffIcon(b: Buffer): IconContentType | null {
  if (b.length >= 8 && b.subarray(0, 8).equals(Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]))) return 'image/png';
  if (b.length >= 12 && b.subarray(0, 4).toString('latin1') === 'RIFF' && b.subarray(8, 12).toString('latin1') === 'WEBP') return 'image/webp';
  const head = b.subarray(0, 2048).toString('utf8').replace(/^﻿/, '').trimStart();
  if ((head.startsWith('<') && /<svg[\s>/]/i.test(b.toString('utf8'))) || head.startsWith('<svg')) return 'image/svg+xml';
  return null;
}
export const isIconContentType = (s: string): s is IconContentType => (ICON_CONTENT_TYPES as readonly string[]).includes(s);

// ───────────────────────────────────────── SVG sanitizing
export class SvgError extends Error {}
const DROP_ELEMENTS = new Set(['script', 'foreignobject', 'iframe', 'embed', 'object', 'handler', 'listener', 'audio', 'video', 'canvas']);
const VOID_OK = /\/\s*$/;
const ATTR_RE = /([^\s=/>"']+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+)))?/g;
const SAFE_DATA = /^data:image\/(png|jpe?g|gif|webp);base64,[A-Za-z0-9+/=\s]+$/i;

function unescapeAttr(v: string): string {
  return v
    .replace(/&#x([0-9a-f]+);?/gi, (_, h: string) => String.fromCodePoint(parseInt(h, 16)))
    .replace(/&#(\d+);?/g, (_, d: string) => String.fromCodePoint(Number(d)))
    .replace(/&colon;/gi, ':')
    .replace(/&tab;|&newline;/gi, '')
    .replace(/&amp;/g, '&');
}
const escAttr = (v: string) => v.replace(/&(?!(amp|lt|gt|quot|apos|#\d+|#x[0-9a-f]+);)/gi, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;');
/** A value referencing something outside the document (url(…) not #fragment, javascript:, external href). */
function unsafeUrlValue(v: string): boolean {
  const u = unescapeAttr(v).replace(/[\s\u0000-\u001f]/g, '').toLowerCase();
  if (u.includes('javascript:') || u.includes('vbscript:')) return true;
  if (u.includes('@import')) return true;
  const urls = [...u.matchAll(/url\(([^)]*)\)/g)].map((m) => m[1]!.replace(/^['"]|['"]$/g, ''));
  return urls.some((x) => !x.startsWith('#') && !SAFE_DATA.test(x));
}
function cleanAttrs(raw: string): string {
  let out = '';
  for (const m of raw.matchAll(ATTR_RE)) {
    const name = m[1]!;
    const lname = name.toLowerCase();
    const value = m[2] ?? m[3] ?? m[4];
    if (lname.startsWith('on')) continue; //                                  event handlers
    if (value === undefined) continue; //                                       XML needs values
    if (lname === 'href' || lname.endsWith(':href') || lname === 'src') {
      const v = unescapeAttr(value).trim();
      if (!(v.startsWith('#') || SAFE_DATA.test(v))) continue;
    }
    if (lname === 'xmlns' || lname.startsWith('xmlns:')) {
      out += ` ${name}="${escAttr(value)}"`;
      continue;
    }
    if (unsafeUrlValue(value)) continue; //                                      style / fill / filter … with external url()
    out += ` ${name}="${escAttr(value)}"`;
  }
  return out;
}
/**
 * Sanitize an SVG document: keeps the element tree, drops <script>, <foreignObject> (and other active elements) with
 * their content, on* attributes, javascript: / external href / xlink:href (only #fragment and data:image raster kept),
 * attributes with external url(…), <style> containing url( / @import, processing instructions, DOCTYPE. Throws
 * SvgError when the document is not a well-formed SVG (root <svg>, balanced tags, no entities).
 */
export function sanitizeSvg(input: string): string {
  const s = input.replace(/^﻿/, '');
  if (/<!ENTITY/i.test(s)) throw new SvgError('SVG with entity declarations is not allowed');
  let i = 0;
  let out = '';
  const stack: string[] = [];
  let drop = 0; // depth inside a dropped element
  let sawRoot = false;
  let rootClosed = false;
  while (i < s.length) {
    const lt = s.indexOf('<', i);
    if (lt < 0) {
      const text = s.slice(i);
      if (stack.length) {
        if (!drop) out += text;
      } else if (text.trim()) throw new SvgError('text outside the <svg> element');
      break;
    }
    const text = s.slice(i, lt);
    if (stack.length) {
      if (!drop) out += text;
    } else if (text.trim()) throw new SvgError('text outside the <svg> element');
    if (s.startsWith('<!--', lt)) {
      const e = s.indexOf('-->', lt + 4);
      if (e < 0) throw new SvgError('unterminated comment');
      i = e + 3;
      continue;
    }
    if (s.startsWith('<![CDATA[', lt)) {
      const e = s.indexOf(']]>', lt);
      if (e < 0) throw new SvgError('unterminated CDATA');
      if (!stack.length) throw new SvgError('CDATA outside the <svg> element');
      if (!drop) out += s.slice(lt, e + 3);
      i = e + 3;
      continue;
    }
    if (s.startsWith('<?', lt)) {
      const e = s.indexOf('?>', lt);
      if (e < 0) throw new SvgError('unterminated processing instruction');
      i = e + 2;
      continue;
    }
    if (s.startsWith('<!', lt)) {
      const e = s.indexOf('>', lt);
      if (e < 0 || s.slice(lt, e).includes('[')) throw new SvgError('DOCTYPE with an internal subset is not allowed');
      i = e + 1;
      continue;
    }
    // tag: find the end ">" outside quotes
    let j = lt + 1;
    let q: string | null = null;
    for (; j < s.length; j++) {
      const ch = s[j]!;
      if (q) {
        if (ch === q) q = null;
      } else if (ch === '"' || ch === "'") q = ch;
      else if (ch === '>') break;
    }
    if (j >= s.length) throw new SvgError('unterminated tag');
    const body = s.slice(lt + 1, j);
    i = j + 1;
    if (body.startsWith('/')) {
      const name = body.slice(1).trim();
      const top = stack.pop();
      if (!top || top !== name) throw new SvgError(`mismatched </${name}>`);
      if (drop) {
        drop--;
        if (drop === 0) continue;
        continue;
      }
      out += `</${name}>`;
      if (!stack.length) rootClosed = true;
      continue;
    }
    const m = /^([A-Za-z_][\w.:-]*)([\s\S]*)$/.exec(body);
    if (!m) throw new SvgError('malformed tag');
    const name = m[1]!;
    let rest = m[2]!;
    const selfClose = VOID_OK.test(rest);
    if (selfClose) rest = rest.replace(VOID_OK, '');
    if (!stack.length) {
      if (rootClosed || sawRoot) throw new SvgError('more than one root element');
      if (name.toLowerCase() !== 'svg' && !name.toLowerCase().endsWith(':svg')) throw new SvgError('the root element must be <svg>');
      sawRoot = true;
    }
    const local = name.toLowerCase().split(':').pop()!;
    if (drop || DROP_ELEMENTS.has(local)) {
      if (!selfClose) {
        stack.push(name);
        drop++;
      }
      continue;
    }
    if (local === 'style' && !selfClose) {
      const close = s.toLowerCase().indexOf(`</${name.toLowerCase()}`, i);
      if (close < 0) throw new SvgError('unterminated <style>');
      const css = s.slice(i, close);
      const e = s.indexOf('>', close);
      i = e + 1;
      if (/url\s*\(|@import|expression\s*\(|javascript:/i.test(css)) continue; // drop the whole <style>
      out += `<${name}${cleanAttrs(rest)}>${css}</${name}>`;
      continue;
    }
    out += `<${name}${cleanAttrs(rest)}${selfClose ? '/>' : '>'}`;
    if (!selfClose) stack.push(name);
    else if (!stack.length) rootClosed = true;
  }
  if (!sawRoot) throw new SvgError('no <svg> element');
  if (stack.length) throw new SvgError(`unclosed <${stack.at(-1)}>`);
  return out;
}

// ───────────────────────────────────────── usage
/** Every entity that references "upload:<iconId>" (services / templates / tags icon, deployments image, accounts img). */
export async function iconUsage(): Promise<Map<string, IconUser[]>> {
  const [svc, tpl, tags, deps, accs] = await Promise.all([
    col.services().select('icon', 'label').get(),
    col.templates().get(),
    col.tags().select('icon', 'label').get(),
    col.deployments().where('deleted', '==', false).select('image', 'label').get(),
    col.accounts().select('img', 'name').get(),
  ]);
  const out = new Map<string, IconUser[]>();
  const add = (v: unknown, u: IconUser) => {
    const id = iconUploadId(v);
    if (id) (out.get(id) ?? out.set(id, []).get(id)!).push(u);
  };
  for (const d of svc.docs) add(d.data().icon, { type: 'service', id: d.id, label: (d.data().label as string) ?? d.id });
  for (const d of tpl.docs) {
    const t = decodeTemplate(d.data());
    add(t.icon, { type: 'template', id: d.id, label: t.label });
  }
  for (const d of tags.docs) add(d.data().icon, { type: 'tag', id: d.id, label: (d.data().label as string) ?? d.id });
  for (const d of deps.docs) add(d.data().image, { type: 'deployment', id: d.id, label: (d.data().label as string) ?? d.id });
  for (const d of accs.docs) add(d.data().img, { type: 'account', id: d.id, label: (d.data().name as string) ?? d.id });
  return out;
}

// ───────────────────────────────────────── validation of icon fields
/** Format check (null / undefined ok). picture = Account.img / Deployment.image (also https URLs). */
export function vIconFormat(v: V, val: unknown, path: string, picture = false) {
  const is = new Issues('draft');
  (picture ? checkPicture : checkIcon)(val, path, is);
  for (const e of is.errors) v.err(e.path, e.code, e.message);
}
/** "upload:<id>" values must reference an existing icon (V_REF). Call after the format check passed. */
export async function vIconRefs(v: V, fields: { path: string; value: unknown }[]) {
  const want = fields.map((f) => ({ ...f, id: iconUploadId(f.value) })).filter((f) => f.id);
  if (!want.length) return;
  const snaps = await db().getAll(...want.map((f) => col.icons().doc(f.id!)));
  want.forEach((f, i) => {
    if (!snaps[i]!.exists) v.err(f.path, 'V_REF', `icon ${f.id} does not exist (upload it first)`);
  });
}

// ───────────────────────────────────────── revision catalogs
/** "upload:<id>" → "data:<contentType>;base64,…" (missing icon → null); other values unchanged. */
export async function embedIcons(values: (string | null)[]): Promise<Map<string, string | null>> {
  const ids = [...new Set(values.map((v) => iconUploadId(v)).filter((x): x is string => !!x))];
  const out = new Map<string, string | null>();
  if (!ids.length) return out;
  const snaps = await db().getAll(...ids.map((id) => col.icons().doc(id)));
  for (const s of snaps) {
    if (!s.exists) {
      out.set(`upload:${s.id}`, null);
      continue;
    }
    const d = s.data() as IconDoc & { _bytes?: Buffer | Uint8Array };
    const bytes = d._bytes ? Buffer.from(d._bytes) : null;
    out.set(`upload:${s.id}`, bytes ? `data:${d.contentType};base64,${bytes.toString('base64')}` : null);
  }
  return out;
}
