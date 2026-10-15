/**
 * Client validation of the simple bodies (registry, service, tag, account, deployment, not-found page, template meta)
 * with the same RE + LIMITS as the API (shared validate.ts). Config documents use validateConfig directly.
 */
import { LIMITS, RE, type ValidationIssue } from '@opm/shared';
import { issue } from './issues';

type Out = ValidationIssue[];
const utf8 = (s: string) => new TextEncoder().encode(s).length;

export function label(v: string, out: Out, path = 'label', max: number = LIMITS.label) {
  if (!v || !v.trim()) out.push(issue(path, 'V_REQUIRED', 'Required'));
  else if (v.length > max) out.push(issue(path, 'V_LENGTH', `At most ${max} characters`));
}
export function description(v: string, out: Out, path = 'description') {
  if (v && v.length > LIMITS.description) out.push(issue(path, 'V_LENGTH', `At most ${LIMITS.description} characters (${v.length})`));
}
export function slug(v: string, out: Out, path: string) {
  if (!v) out.push(issue(path, 'V_REQUIRED', 'Required'));
  else if (!RE.slug.test(v)) out.push(issue(path, 'V_FORMAT', '2–40 characters: a–z, 0–9 and "-", starting with a letter'));
}
/** Icon value (null = the default icon): lucide:<name>, upload:<iconId> or a legacy bare Lucide name. */
export function icon(v: string | null | undefined, out: Out, path = 'icon', required = false) {
  if (!v) {
    if (required) out.push(issue(path, 'V_REQUIRED', 'Required'));
    return;
  }
  if (!RE.icon.test(v)) out.push(issue(path, 'V_FORMAT', 'Pick an icon (lucide:<name> or an uploaded icon)'));
}
/** Account.img / Deployment.image: null, an icon reference or a legacy https URL. */
export function picture(v: string | null | undefined, out: Out, path: string) {
  if (!v) return;
  if (!RE.picture.test(v)) out.push(issue(path, 'V_FORMAT', 'Pick an icon or upload an image'));
}
export function httpsUrlOptional(v: string | null | undefined, out: Out, path: string) {
  if (!v) return;
  if (!RE.httpsUrlAny.test(v)) out.push(issue(path, 'V_FORMAT', 'Must be an https:// URL'));
}

export function validateRegistry(b: { label: string; url: string; username: string; password: string; anonymous?: boolean }, isNew: boolean): Out {
  const out: Out = [];
  label(b.label, out);
  if (b.url && !RE.httpsUrl.test(b.url)) out.push(issue('url', 'V_FORMAT', 'https://host[:port] without a path'));
  if (b.anonymous) return out;
  if (!b.username) out.push(issue('username', 'V_REQUIRED', 'Required'));
  else if (b.username.length > 255) out.push(issue('username', 'V_LENGTH', 'At most 255 characters'));
  if (isNew && !b.password) out.push(issue('password', 'V_REQUIRED', 'Required'));
  if (b.password && b.password.length > 8192) out.push(issue('password', 'V_LENGTH', 'At most 8192 characters'));
  return out;
}
export function validateService(b: { label: string; serviceSlug?: string; icon: string | null; description: string }, isNew: boolean): Out {
  const out: Out = [];
  label(b.label, out);
  if (isNew) slug(b.serviceSlug ?? '', out, 'serviceSlug');
  icon(b.icon, out);
  description(b.description, out);
  return out;
}
export function validateTag(b: { tagSlug?: string; label: string; icon: string | null; color: string }, isNew: boolean): Out {
  const out: Out = [];
  if (isNew) slug(b.tagSlug ?? '', out, 'tagSlug');
  label(b.label, out);
  icon(b.icon, out);
  if (!RE.color.test(b.color)) out.push(issue('color', 'V_FORMAT', 'A colour like #4f46e5 (lower-case hex)'));
  return out;
}
export function validateProfile(b: { name: string; phone: string; img: string | null; email?: string }, withEmail: boolean): Out {
  const out: Out = [];
  label(b.name, out, 'name');
  if (withEmail) {
    if (!b.email) out.push(issue('email', 'V_REQUIRED', 'Required'));
    else if (!RE.email.test(b.email) || b.email.length > 254) out.push(issue('email', 'V_FORMAT', 'Not an email address'));
  }
  if (b.phone && !RE.e164.test(b.phone)) out.push(issue('phone', 'V_FORMAT', 'E.164, e.g. +919812345678'));
  picture(b.img, out, 'img');
  return out;
}
export function validateDeploymentMeta(b: { label: string; image: string | null; tagSlugs: string[] }): Out {
  const out: Out = [];
  label(b.label, out);
  picture(b.image, out, 'image');
  if (b.tagSlugs.length > 20) out.push(issue('tagSlugs', 'V_TOO_MANY', 'At most 20 tags'));
  return out;
}
/** §21 #106 a manually typed entity slug (template / deployment): RE.slug; empty = derived from the label by the server */
export function slugIssues(slug: string, manual: boolean, out: Out) {
  if (manual && slug && !/^[a-z][a-z0-9-]{1,39}$/.test(slug)) out.push(issue('slug', 'V_FORMAT', '2–40 characters a-z, 0-9 and "-", starting with a letter'));
}
export function validateNotFoundPage(b: { label: string; htmlCode: string }): Out {
  const out: Out = [];
  label(b.label, out);
  if (!b.htmlCode || !b.htmlCode.trim()) out.push(issue('htmlCode', 'V_REQUIRED', 'Required'));
  else if (utf8(b.htmlCode) > LIMITS.notFoundPageBytes) out.push(issue('htmlCode', 'V_LENGTH', `At most ${LIMITS.notFoundPageBytes} bytes (256 KiB)`));
  return out;
}
export function validateTemplateMeta(b: { label: string; icon: string | null; description: string }): Out {
  const out: Out = [];
  label(b.label, out);
  icon(b.icon, out);
  description(b.description, out);
  return out;
}
export const utf8Bytes = utf8;
