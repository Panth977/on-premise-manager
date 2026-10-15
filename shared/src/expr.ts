/**
 * OPM expression engine ({{ … }} blocks). Normative grammar and semantics: SPEC-final.md §10 (filters §10.4; hash filters §21 #103, shared/src/hash.ts).
 * The Go port (agent/internal/expr) must give the same results for testdata/expr-vectors.json.
 *
 * Positions are 0-based indexes in Unicode code points (not UTF-16 units, not bytes).
 */
import { canonicalize, formatNumber } from './canonical.js';
import { MOSQUITTO_DEFAULT_ITERATIONS, MOSQUITTO_MAX_ITERATIONS, mosquittoHash, mosquittoSalt, sha256, sha512, toHex, utf8 } from './hash.js';

export const ROOTS = ['var', 'secret', 'file', 'site', 'self', 'deployment', 'container', 'containers'] as const;
export type Root = (typeof ROOTS)[number];
export const FILTERS = ['default', 'join', 'upper', 'lower', 'trim', 'base64', 'url', 'int', 'json', 'sha256', 'sha512', 'mosquitto_passwd'] as const;
export type FilterName = (typeof FILTERS)[number];
/**
 * §21 #103: filters added after the first agents shipped. An agent older than the version refuses a revision that uses
 * one (E_UNKNOWN_FILTER at staging) — the cloud warns per site (`exprMinAgent`, validate.ts `siteAgentNeeds`).
 */
export const FILTER_MIN_AGENT: Partial<Record<FilterName, string>> = { sha256: '0.13.1', sha512: '0.13.1', mosquitto_passwd: '0.13.1' };
/** §21 #103: password-hash filters only where secrets are allowed (their input is normally a secret). */
export const PASSWORD_FILTERS: readonly FilterName[] = ['mosquitto_passwd'];
/**
 * §21 #103: filters whose output is a salted, stretched one-way password hash meant to be stored in a readable file
 * (Mosquitto password_file). A secret that reaches the result ONLY through one of them is not "in clear": the result
 * stays secret (masked, never logged) but `secretHashed` is set, and a rendered File with only such secrets is written
 * 0444 instead of 0400 — Mosquitto opens password_file AFTER dropping to its own user, so a root-owned 0400 file is
 * unreadable to it.
 */
export const HASHING_FILTERS: readonly FilterName[] = ['mosquitto_passwd'];

/** The kind of field a value belongs to. It decides the allowed roots and the result type. */
export type ExprField = 'env' | 'imageEnv' | 'host' | 'hostPort' | 'imagePort' | 'edge' | 'mountPath' | 'file' | 'devtool';
export const FIELD_ROOTS: Record<ExprField, readonly Root[]> = {
  env: ['var', 'secret', 'file', 'site', 'self', 'deployment', 'container', 'containers'],
  imageEnv: ['var', 'file', 'site', 'self', 'deployment', 'container', 'containers'],
  host: ['var', 'deployment'],
  hostPort: ['var', 'deployment'],
  imagePort: ['var', 'deployment'],
  edge: ['var', 'deployment', 'self'],
  /** ImageVolume.mountPath and Container.volume[].mountPath (result: absolute path, not "/" — V_PATH in validate.ts) */
  mountPath: ['var', 'deployment', 'self', 'container'],
  /** FileDef content with render 'expressions', resolved per mounting container (multi-line text; blocks stay on one line) */
  file: ['var', 'secret', 'site', 'self', 'deployment', 'containers'], // round D: rendered once per site (no container.*, no file.*); containers.* = site-level facts (§21 #88)
  /**
   * §21 #99: dev-tool preset values (username, database, clientId, apiKeyHeader, password / token expressions, default
   * header values, url target) and agent dev-tool expressions, resolved on the site from the head config. The only field
   * where `containers.<slug>.env.<NAME>` (another container's FINAL env value) is allowed: no container value can
   * reference it, so there are no cross-container cycles.
   */
  devtool: ['var', 'secret', 'site', 'self', 'deployment', 'containers'],
};
/** Fields where `secret.*` and secret-valued `container.env.<NAME>` / `containers.<slug>.env.<NAME>` are allowed (rendered files: read-only mount, mode 0400). */
export const SECRET_FIELDS: readonly ExprField[] = ['env', 'file', 'devtool'];
/** §21 #99: the only fields where `containers.<containerSlug>.env.<NAME>` is allowed. */
export const CONTAINERS_ENV_FIELDS: readonly ExprField[] = ['devtool'];
export const NUMERIC_FIELDS: readonly ExprField[] = ['hostPort', 'imagePort'];

export type ExprErrorCode =
  | 'E_SYNTAX'
  | 'E_UNKNOWN_PATH'
  | 'E_NOT_ALLOWED'
  | 'E_UNKNOWN_FILTER'
  | 'E_TYPE'
  | 'E_NULL'
  | 'E_NO_HOSTPORT'
  | 'E_FILE_NOT_MOUNTED'
  | 'E_CYCLE'
  /** round E: the value depends on a REQUIRED variable / secret that is set at deployment time (template / draft):
   *  never an issue (Issues drops it); previews show DEPLOY_PLACEHOLDER. Never raised in agent mode. */
  | 'E_DEFERRED';

/**
 * Stand-in value of a REQUIRED variable / secret without a value outside agent mode (template, draft, publish checks):
 * the deployment must set it before publishing (V_VAR_REQUIRED / V_SECRET_REQUIRED on the variable itself), so its usages
 * are not reported. Expressions keep evaluating with this text; a result that contains it is `deferred`.
 */
export const DEPLOY_PLACEHOLDER = '‹set at deployment›';
export const isDeferredValue = (v: unknown): boolean => typeof v === 'string' && v.includes(DEPLOY_PLACEHOLDER);

export class ExprError extends Error {
  constructor(
    public readonly code: ExprErrorCode,
    message: string,
    public readonly pos: number,
  ) {
    super(message);
    this.name = 'ExprError';
  }
}

export type Literal = string | number | boolean;
export interface LitNode {
  kind: 'lit';
  value: Literal;
  pos: number;
}
export interface PathNode {
  kind: 'path';
  segments: string[];
  pos: number;
}
export interface FilterNode {
  name: FilterName;
  args: Literal[];
  pos: number;
}
export interface BlockSeg {
  kind: 'block';
  primary: LitNode | PathNode;
  filters: FilterNode[];
  start: number; // position of the first "{"
  end: number; //   position after the last "}"
}
export interface TextSeg {
  kind: 'text';
  text: string;
  start: number;
}
export type Segment = TextSeg | BlockSeg;

/** Values a path can resolve to. */
export type ExprValue = string | number | boolean | null | ExprValue[] | { [k: string]: ExprValue };

/**
 * Everything the agent knows when it resolves one value. Missing keys = unknown slugs.
 * - file[slug].path: the mountPath of that file in THIS container, or null when the container does not mount it.
 * - site[slug].port[serviceSlug][portName]: the resolved hostPort, or null when hostPort is null.
 * - secret[slug]: the value, or null when the secret has no value.
 */
export interface ExprContext {
  deployment: { id: string };
  /** host: null = auto host not known yet (site not installed) → E_NULL; the cloud passes autoHostPlaceholder(slug) instead */
  self: { slug: string; host: string | null };
  /**
   * env[NAME]: the FINAL value of env var NAME in this container (image default overridden by the container env,
   * expressions resolved); value null = known name without a value; secret = secretRef / secret.* value.
   * resolveContainer() fills it lazily in dependency order (E_CYCLE on cycles).
   */
  container?: { slug: string; env?: Record<string, { value: string | null; secret: boolean }> } | null;
  var: Record<string, ExprValue>;
  secret?: Record<string, string | null>;
  file?: Record<string, { path: string | null }>;
  /** site[slug].port[containerSlug][portName]; host null = auto host not known yet */
  site?: Record<string, { host: string | null; port: Record<string, Record<string, number | null>> }>;
  /**
   * §21 #88 containers.<containerSlug>.host / .port.<portName> (+ §21 #99 .env.<NAME>, field devtool only): the containers of THIS site (self included).
   * host = the DNS name on the site network (the network alias = containerSlug; resolves only from containers of the same
   * site network, in both docker naming modes); port[portName] = the INTERNAL (container) port of that image port (null =
   * it does not resolve → E_NULL); enabled false → the value still resolves (validate warns V_CONTAINER_REF_DISABLED).
   */
  containers?: Record<string, ContainerPeer>;
}
/** One container of the site as seen by containers.<slug>.* (§21 #88). */
export interface ContainerPeer {
  host: string;
  enabled: boolean;
  port: Record<string, number | null>;
  /**
   * §21 #99 containers.<slug>.env.<NAME> (field devtool only; siteDevtoolExprContext fills it): the FINAL env value of
   * that container — the image default overridden by the container env, its expressions resolved (resolveContainer).
   * value null = known name without a value (E_NULL unless | default); secret = secretRef / secret.* value;
   * error = that value failed to resolve (the code is re-raised at the reference). Absent = no env known (any NAME →
   * E_UNKNOWN_PATH).
   */
  env?: Record<string, PeerEnv>;
}
/** One env var of another container (containers.<slug>.env.<NAME>, §21 #99). */
export interface PeerEnv {
  value: string | null;
  secret: boolean;
  error?: { code: ExprErrorCode; message: string } | null;
}

/**
 * The text the CLOUD uses for site.<slug>.host / self.host while the site host is auto (SiteConfig.host null) and the
 * site has not reported an address: "<auto:slug>". It never reaches a site (agents use the real address). GUIs show it
 * as "auto · detected on install".
 */
export const autoHostPlaceholder = (siteSlug: string) => `<auto:${siteSlug}>`;
export const AUTO_HOST_RE = /<auto:([a-z][a-z0-9-]{1,39})>/g;

/** 1-based line + column (code points) of a 0-based code-point position in src (staging errors: file:line:col). */
export function exprLineCol(src: string, pos: number): { line: number; col: number } {
  const cp = Array.from(src);
  let line = 1;
  let col = 1;
  for (let i = 0; i < pos && i < cp.length; i++) {
    if (cp[i] === '\n') {
      line++;
      col = 1;
    } else col++;
  }
  return { line, col };
}

export interface ResolveResult {
  /** final text (numeric fields: the decimal text of the integer) */
  value: string;
  /** numeric fields only */
  number: number | null;
  /** same as value but every secret.* contribution replaced by "••••" */
  masked: string;
  /** true when any secret.* path was used: store / show as a secret */
  secret: boolean;
  /** the paths used, in order, e.g. ["secret.db-password", "site.db.host"] */
  refs: string[];
  /** true when a required-but-unset variable / secret (DEPLOY_PLACEHOLDER) contributed: the value is known only at deployment */
  deferred?: boolean;
  /** §21 #103: secret, but every secret part went through a HASHING_FILTERS filter (no secret in clear); absent = false */
  secretHashed?: boolean;
}

// ───────────────────────────────────────────────────────────── parser
const isLetter = (c: string | undefined) => c !== undefined && /^[A-Za-z]$/.test(c);
const isDigit = (c: string | undefined) => c !== undefined && c >= '0' && c <= '9';
const isSegChar = (c: string | undefined) => c !== undefined && (isLetter(c) || isDigit(c) || c === '-' || c === '_');
const isWs = (c: string | undefined) => c === ' ' || c === '\t';

class Parser {
  readonly cp: string[];
  constructor(src: string) {
    this.cp = Array.from(src);
  }
  at(i: number): string | undefined {
    return this.cp[i];
  }
  skipWs(i: number): number {
    while (isWs(this.cp[i])) i++;
    return i;
  }
  /** a newline inside a block is a syntax error */
  noNewline(i: number) {
    const c = this.cp[i];
    if (c === '\n' || c === '\r') throw new ExprError('E_SYNTAX', 'a {{ }} block cannot span lines', i);
  }

  template(): Segment[] {
    const out: Segment[] = [];
    let i = 0;
    let buf = '';
    let bufStart = 0;
    const n = this.cp.length;
    while (i < n) {
      if (this.cp[i] === '{' && this.cp[i + 1] === '{') {
        if (buf) out.push({ kind: 'text', text: buf, start: bufStart });
        buf = '';
        const start = i;
        const { block, next } = this.block(i);
        block.start = start;
        out.push(block);
        i = next;
        bufStart = i;
      } else {
        if (!buf) bufStart = i;
        buf += this.cp[i];
        i++;
      }
    }
    if (buf) out.push({ kind: 'text', text: buf, start: bufStart });
    return out;
  }

  block(i: number): { block: BlockSeg; next: number } {
    i += 2; // "{{"
    i = this.skipWs(i);
    this.noNewline(i);
    const p = this.primary(i);
    i = p.next;
    const filters: FilterNode[] = [];
    for (;;) {
      i = this.skipWs(i);
      this.noNewline(i);
      if (this.cp[i] !== '|') break;
      i = this.skipWs(i + 1);
      this.noNewline(i);
      const f = this.filter(i);
      filters.push(f.node);
      i = f.next;
    }
    if (!(this.cp[i] === '}' && this.cp[i + 1] === '}')) throw new ExprError('E_SYNTAX', 'expected "}}"', i);
    i += 2;
    return { block: { kind: 'block', primary: p.node, filters, start: 0, end: i }, next: i };
  }

  primary(i: number): { node: LitNode | PathNode; next: number } {
    const c = this.cp[i];
    if (c === '"') {
      let j = i + 1;
      let v = '';
      for (;;) {
        const d = this.cp[j];
        if (d === undefined || d === '\n' || d === '\r') throw new ExprError('E_SYNTAX', 'unterminated string', i);
        if (d === '"') break;
        if (d === '\\') {
          const e = this.cp[j + 1];
          if (e === '"' || e === '\\') {
            v += e;
            j += 2;
            continue;
          }
          throw new ExprError('E_SYNTAX', 'only \\" and \\\\ are valid escapes', j);
        }
        v += d;
        j++;
      }
      return { node: { kind: 'lit', value: v, pos: i }, next: j + 1 };
    }
    if (isDigit(c) || (c === '-' && isDigit(this.cp[i + 1]))) {
      let j = i;
      if (this.cp[j] === '-') j++;
      while (isDigit(this.cp[j])) j++;
      if (this.cp[j] === '.' && isDigit(this.cp[j + 1])) {
        j++;
        while (isDigit(this.cp[j])) j++;
      }
      const text = this.cp.slice(i, j).join('');
      return { node: { kind: 'lit', value: Number(text), pos: i }, next: j };
    }
    if (isLetter(c)) {
      let j = i;
      while (isSegChar(this.cp[j])) j++;
      const first = this.cp.slice(i, j).join('');
      const segments = [first];
      while (this.cp[j] === '.' && (isLetter(this.cp[j + 1]) || isDigit(this.cp[j + 1]))) {
        const s = j + 1;
        j = s;
        while (isSegChar(this.cp[j])) j++;
        segments.push(this.cp.slice(s, j).join(''));
      }
      if (segments.length === 1 && (first === 'true' || first === 'false')) return { node: { kind: 'lit', value: first === 'true', pos: i }, next: j };
      if (!(ROOTS as readonly string[]).includes(first)) throw new ExprError('E_UNKNOWN_PATH', `unknown root "${first}" (use ${ROOTS.join(', ')})`, i);
      if (segments.length === 1) throw new ExprError('E_SYNTAX', `"${first}" needs a "." and a name`, j);
      return { node: { kind: 'path', segments, pos: i }, next: j };
    }
    throw new ExprError('E_SYNTAX', 'expected a path, a string, a number, or true / false', i);
  }

  filter(i: number): { node: FilterNode; next: number } {
    let j = i;
    if (!(this.cp[j] !== undefined && this.cp[j]! >= 'a' && this.cp[j]! <= 'z')) throw new ExprError('E_SYNTAX', 'expected a filter name after "|"', i);
    while (this.cp[j] !== undefined && ((this.cp[j]! >= 'a' && this.cp[j]! <= 'z') || isDigit(this.cp[j]) || this.cp[j] === '_')) j++;
    const name = this.cp.slice(i, j).join('');
    if (!(FILTERS as readonly string[]).includes(name)) throw new ExprError('E_UNKNOWN_FILTER', `unknown filter "${name}" (use ${FILTERS.join(', ')})`, i);
    const node: FilterNode = { name: name as FilterName, args: [], pos: i };
    if (this.cp[j] === '(') {
      j = this.skipWs(j + 1);
      this.noNewline(j);
      if (this.cp[j] !== ')') {
        for (;;) {
          const a = this.primary(j);
          if (a.node.kind !== 'lit') throw new ExprError('E_SYNTAX', 'filter arguments must be literals', j);
          node.args.push(a.node.value);
          j = this.skipWs(a.next);
          this.noNewline(j);
          if (this.cp[j] === ',') {
            j = this.skipWs(j + 1);
            this.noNewline(j);
            continue;
          }
          break;
        }
      }
      if (this.cp[j] !== ')') throw new ExprError('E_SYNTAX', 'expected ")"', j);
      j++;
    }
    checkArity(node);
    return { node, next: j };
  }
}

function checkArity(f: FilterNode) {
  const n = f.args.length;
  if (f.name === 'default') {
    if (n !== 1) throw new ExprError('E_SYNTAX', 'default needs exactly 1 argument', f.pos);
    return;
  }
  if (f.name === 'join') {
    if (n > 1) throw new ExprError('E_SYNTAX', 'join takes at most 1 argument', f.pos);
    if (n === 1 && typeof f.args[0] !== 'string') throw new ExprError('E_SYNTAX', 'join needs a string argument', f.pos);
    return;
  }
  if (f.name === 'mosquitto_passwd') {
    // mosquitto_passwd(format = 7, iterations = 101): format 7 = sha512-pbkdf2 (Mosquitto 2.x), 6 = salted sha512 (1.x)
    if (n > 2) throw new ExprError('E_SYNTAX', 'mosquitto_passwd takes at most 2 arguments: (format 7 | 6, iterations)', f.pos);
    if (n >= 1 && f.args[0] !== 7 && f.args[0] !== 6) throw new ExprError('E_SYNTAX', 'mosquitto_passwd: the format must be 7 (Mosquitto 2.x, sha512-pbkdf2) or 6 (1.x, salted sha512)', f.pos);
    if (n === 2) {
      if (f.args[0] === 6) throw new ExprError('E_SYNTAX', 'mosquitto_passwd(6) has no iterations (salted sha512 runs once)', f.pos);
      const it = f.args[1];
      if (typeof it !== 'number' || !Number.isInteger(it) || it < 1 || it > MOSQUITTO_MAX_ITERATIONS)
        throw new ExprError('E_SYNTAX', `mosquitto_passwd: iterations must be an integer 1–${MOSQUITTO_MAX_ITERATIONS}`, f.pos);
    }
    return;
  }
  if (n !== 0) throw new ExprError('E_SYNTAX', `${f.name} takes no arguments`, f.pos);
}

/** Parse a value into text and block segments. Throws ExprError (E_SYNTAX, E_UNKNOWN_PATH root, E_UNKNOWN_FILTER). */
export function parseTemplate(src: string): Segment[] {
  return new Parser(src).template();
}

/** true when the string holds at least one "{{" (i.e. it is meant as an expression). */
export function isExpr(v: unknown): v is string {
  return typeof v === 'string' && v.includes('{{');
}

// ───────────────────────────────────────────────────────────── path shape (no context)
/** Checks the shape of a path for its root (number of segments, fixed names). Throws E_UNKNOWN_PATH. */
export function checkPathShape(p: PathNode): void {
  const [root, ...rest] = p.segments;
  const bad = () => new ExprError('E_UNKNOWN_PATH', `unknown path "${p.segments.join('.')}"`, p.pos);
  switch (root) {
    case 'var':
    case 'secret':
      if (rest.length !== 1) throw bad();
      return;
    case 'file':
      if (rest.length !== 2 || rest[1] !== 'path') throw bad();
      return;
    case 'site':
      if (rest.length === 2 && rest[1] === 'host') return;
      if (rest.length === 4 && rest[1] === 'port') return;
      throw bad();
    case 'self':
      if (rest.length !== 1 || (rest[0] !== 'slug' && rest[0] !== 'host')) throw bad();
      return;
    case 'deployment':
      if (rest.length !== 1 || rest[0] !== 'id') throw bad();
      return;
    case 'container':
      if (rest.length === 2 && rest[0] === 'env') return;
      if (rest.length !== 1 || rest[0] !== 'slug') throw bad();
      return;
    case 'containers':
      if (rest.length === 2 && rest[1] === 'host') return;
      if (rest.length === 3 && rest[1] === 'port') return;
      if (rest.length === 3 && rest[1] === 'env') return; // §21 #99 (field devtool only, checkAllowed)
      throw bad();
    default:
      throw bad();
  }
}

function checkAllowed(p: PathNode, field: ExprField) {
  const root = p.segments[0] as Root;
  if (!FIELD_ROOTS[field].includes(root)) {
    if (field === 'file' && (root === 'container' || root === 'file'))
      throw new ExprError('E_NOT_ALLOWED', `"${root}.*" is not allowed in ${FIELD_LABEL[field]}: files are shared by all containers on the site — use a variable instead`, p.pos);
    throw new ExprError('E_NOT_ALLOWED', `"${root}.*" is not allowed in ${FIELD_LABEL[field]}`, p.pos);
  }
  // §21 #99: another container's env only in dev-tool presets (a container value reading another container's env could
  // form cross-container cycles; this container's own env is container.env.<NAME>)
  if (root === 'containers' && p.segments[2] === 'env' && !CONTAINERS_ENV_FIELDS.includes(field))
    throw new ExprError(
      'E_NOT_ALLOWED',
      `"containers.<container>.env.*" is only allowed in dev-tool presets, not in ${FIELD_LABEL[field]} (it reads another container's env; use container.env.<NAME> for this container, or a variable)`,
      p.pos,
    );
}
/** §21 #103: a password-hash filter outside the secret fields → E_NOT_ALLOWED at the filter name. */
function checkFilterAllowed(f: FilterNode, field: ExprField) {
  if (PASSWORD_FILTERS.includes(f.name) && !SECRET_FIELDS.includes(field))
    throw new ExprError('E_NOT_ALLOWED', `filter "${f.name}" is only allowed where secrets are (container env, file content, dev-tool presets), not in ${FIELD_LABEL[field]}`, f.pos);
}
export const FIELD_LABEL: Record<ExprField, string> = {
  env: 'Container.env[].value',
  imageEnv: 'ImageEnv.value',
  host: 'SiteConfig.host',
  hostPort: 'Container.port[].hostPort',
  imagePort: 'ImagePort.port',
  edge: 'edge fields (hostname, redirect.to, setHeaders)',
  mountPath: 'volume mountPath',
  file: 'file content',
  devtool: 'dev-tool presets',
};

function checkNumericShape(segs: Segment[]) {
  const blocks = segs.filter((s) => s.kind === 'block');
  if (blocks.length !== 1 || segs.some((s) => s.kind === 'text' && s.text.trim() !== '')) {
    throw new ExprError('E_TYPE', 'a number field must be ONE whole block, for example {{ var.api-port }}', 0);
  }
}

/**
 * Validate without values: syntax, filters, allowed roots, path shapes, numeric-field form.
 * Returns the list of errors (at most one: the first error), [] when the value is fine.
 * Unknown slugs are NOT checked here (use resolve() with a context for that).
 */
export function checkExpr(src: string, field: ExprField): ExprError[] {
  try {
    const segs = parseTemplate(src);
    if (NUMERIC_FIELDS.includes(field)) checkNumericShape(segs);
    for (const s of segs) {
      if (s.kind !== 'block') continue;
      if (s.primary.kind === 'path') {
        checkAllowed(s.primary, field);
        checkPathShape(s.primary);
      }
      for (const f of s.filters) checkFilterAllowed(f, field);
    }
    return [];
  } catch (e) {
    if (e instanceof ExprError) return [e];
    throw e;
  }
}

/** §21 #103: the filter names a value uses (unique, in order of first use); [] when it does not parse. */
export function exprFilters(src: string): FilterName[] {
  const out: FilterName[] = [];
  try {
    for (const s of parseTemplate(src)) if (s.kind === 'block') for (const f of s.filters) if (!out.includes(f.name)) out.push(f.name);
  } catch {
    return [];
  }
  return out;
}
/** §21 #103: the oldest agent version that can resolve this value (null = every agent), from FILTER_MIN_AGENT. */
export function exprMinAgent(src: string): string | null {
  let min: string | null = null;
  for (const f of exprFilters(src)) {
    const v = FILTER_MIN_AGENT[f];
    if (v && (min === null || cmpVersion(v, min) > 0)) min = v;
  }
  return min;
}
function cmpVersion(a: string, b: string): number {
  const pa = a.split('.').map(Number);
  const pb = b.split('.').map(Number);
  for (let i = 0; i < 3; i++) if ((pa[i] ?? 0) !== (pb[i] ?? 0)) return (pa[i] ?? 0) - (pb[i] ?? 0);
  return 0;
}

/** The paths ("root.a.b") used by a value, in order. Throws on syntax errors. */
export function exprRefs(src: string): string[] {
  const out: string[] = [];
  for (const s of parseTemplate(src)) if (s.kind === 'block' && s.primary.kind === 'path') out.push(s.primary.segments.join('.'));
  return out;
}

// ───────────────────────────────────────────────────────────── evaluator
/**
 * Lazy lookups used by resolveContainer(): container.env.<NAME> and file.<slug>.path resolve other values of the same
 * container on demand (dependency order + cycle check). Without hooks the plain ExprContext data is used.
 */
export interface ResolveHooks {
  env?: (name: string, p: PathNode) => { value: string | null; secret: boolean } | undefined;
  file?: (slug: string, p: PathNode) => { path: string | null } | undefined;
}

function lookup(p: PathNode, ctx: ExprContext, field: ExprField, hooks?: ResolveHooks): { v: ExprValue; secret: boolean } {
  checkAllowed(p, field);
  checkPathShape(p);
  const [root, ...rest] = p.segments as [Root, ...string[]];
  const bad = () => new ExprError('E_UNKNOWN_PATH', `unknown path "${p.segments.join('.')}"`, p.pos);
  const has = (o: object | undefined | null, k: string) => !!o && Object.prototype.hasOwnProperty.call(o, k);
  switch (root) {
    case 'var':
      if (!has(ctx.var, rest[0]!)) throw bad();
      return { v: ctx.var[rest[0]!] ?? null, secret: false };
    case 'secret':
      if (!has(ctx.secret, rest[0]!)) throw bad();
      return { v: ctx.secret![rest[0]!] ?? null, secret: true };
    case 'file': {
      const f = hooks?.file ? hooks.file(rest[0]!, p) : has(ctx.file, rest[0]!) ? ctx.file![rest[0]!]! : undefined;
      if (!f) throw bad();
      if (f.path === null) throw new ExprError('E_FILE_NOT_MOUNTED', `this container does not mount file "${rest[0]}"`, p.pos);
      return { v: f.path, secret: false };
    }
    case 'site': {
      if (!has(ctx.site, rest[0]!)) throw bad();
      const st = ctx.site![rest[0]!]!;
      if (rest.length === 2) {
        if (st.host === null) throw new ExprError('E_NULL', `site ${rest[0]} has host auto and has not reported its address yet (install the site, or set its host)`, p.pos);
        return { v: st.host, secret: false };
      }
      if (!has(st.port, rest[2]!) || !has(st.port[rest[2]!], rest[3]!)) throw bad();
      const hp = st.port[rest[2]!]![rest[3]!];
      if (hp === null || hp === undefined)
        throw new ExprError('E_NO_HOSTPORT', `site ${rest[0]}: ${rest[2]}.${rest[3]} has hostPort null, so other sites cannot reach it`, p.pos);
      return { v: hp, secret: false };
    }
    case 'self':
      if (rest[0] === 'host' && ctx.self.host === null) throw new ExprError('E_NULL', `site ${ctx.self.slug} has host auto and its address is not known yet`, p.pos);
      return { v: rest[0] === 'slug' ? ctx.self.slug : ctx.self.host, secret: false };
    case 'deployment':
      return { v: ctx.deployment.id, secret: false };
    case 'containers': {
      if (!has(ctx.containers, rest[0]!)) throw new ExprError('E_UNKNOWN_PATH', `no container "${rest[0]}" on this site`, p.pos);
      const c = ctx.containers![rest[0]!]!;
      if (rest[1] === 'host') return { v: c.host, secret: false };
      if (rest[1] === 'env') {
        const name = rest[2]!;
        const e = has(c.env, name) ? c.env![name]! : undefined;
        if (!e) throw new ExprError('E_UNKNOWN_PATH', `container ${rest[0]} has no env var "${name}" (image env or container env)`, p.pos);
        if (e.error) throw new ExprError(e.error.code, `containers.${rest[0]}.env.${name}: ${e.error.message}`, p.pos);
        if (e.secret && !SECRET_FIELDS.includes(field))
          throw new ExprError('E_NOT_ALLOWED', `env var ${name} of container ${rest[0]} holds a secret; secrets are not allowed in ${FIELD_LABEL[field]}`, p.pos);
        return { v: e.value, secret: e.secret };
      }
      if (!has(c.port, rest[2]!)) throw new ExprError('E_UNKNOWN_PATH', `container ${rest[0]} has no port "${rest[2]}" (its image ports: ${Object.keys(c.port).join(', ') || 'none'})`, p.pos);
      const n = c.port[rest[2]!];
      if (n === null || n === undefined) throw new ExprError('E_NULL', `port ${rest[2]} of container ${rest[0]} has no number (its image port does not resolve)`, p.pos);
      return { v: n, secret: false };
    }
    case 'container': {
      if (!ctx.container) throw bad();
      if (rest[0] !== 'env') return { v: ctx.container.slug, secret: false };
      const name = rest[1]!;
      const e = hooks?.env ? hooks.env(name, p) : has(ctx.container.env, name) ? ctx.container.env![name]! : undefined;
      if (!e) throw new ExprError('E_UNKNOWN_PATH', `this container has no env var "${name}" (image env or container env)`, p.pos);
      if (e.secret && !SECRET_FIELDS.includes(field))
        throw new ExprError('E_NOT_ALLOWED', `env var ${name} holds a secret; secrets are not allowed in ${FIELD_LABEL[field]}`, p.pos);
      return { v: e.value, secret: e.secret };
    }
  }
}

const isStrNum = (v: ExprValue): v is string | number => typeof v === 'string' || typeof v === 'number';
function typeName(v: ExprValue): string {
  if (v === null) return 'null';
  if (Array.isArray(v)) return 'a list';
  if (typeof v === 'object') return 'an object';
  return typeof v === 'string' ? 'a string' : typeof v === 'number' ? 'a number' : 'a boolean';
}
function scalarText(v: ExprValue): string | undefined {
  if (typeof v === 'string') return v;
  if (typeof v === 'number') return formatNumber(v);
  if (typeof v === 'boolean') return v ? 'true' : 'false';
  return undefined;
}
const INT_RE = /^\s*-?\d+(\.\d+)?\s*$/;
function utf8Base64(s: string): string {
  const bytes = new TextEncoder().encode(s);
  let bin = '';
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin);
}
/** RFC 3986: keep A-Z a-z 0-9 - _ . ~ ; percent-encode every other UTF-8 byte with upper-case hex. */
export function urlEncode(s: string): string {
  let out = '';
  for (const b of new TextEncoder().encode(s)) {
    const c = String.fromCharCode(b);
    if (/^[A-Za-z0-9\-_.~]$/.test(c)) out += c;
    else out += '%' + b.toString(16).toUpperCase().padStart(2, '0');
  }
  return out;
}

function applyFilter(f: FilterNode, v: ExprValue, ctx: ExprContext): ExprValue {
  const t = (ok: boolean) => {
    if (!ok) throw new ExprError('E_TYPE', `filter "${f.name}" cannot take ${typeName(v)}`, f.pos);
  };
  switch (f.name) {
    case 'default':
      return v === null || v === '' ? (f.args[0] as ExprValue) : v;
    case 'join': {
      t(Array.isArray(v));
      const sep = f.args.length ? String(f.args[0]) : ',';
      return (v as ExprValue[])
        .map((x) => {
          const s = scalarText(x);
          if (s === undefined) throw new ExprError('E_TYPE', `filter "join" cannot join ${typeName(x)} items`, f.pos);
          return s;
        })
        .join(sep);
    }
    case 'upper':
      t(isStrNum(v));
      return scalarText(v)!.toUpperCase();
    case 'lower':
      t(isStrNum(v));
      return scalarText(v)!.toLowerCase();
    case 'trim':
      t(typeof v === 'string');
      return (v as string).trim();
    case 'base64':
      t(isStrNum(v));
      return utf8Base64(scalarText(v)!);
    case 'url':
      t(isStrNum(v));
      return urlEncode(scalarText(v)!);
    case 'int': {
      t(isStrNum(v));
      if (typeof v === 'number') return Math.trunc(v) || 0;
      if (!INT_RE.test(v as string)) throw new ExprError('E_TYPE', `"${v}" is not an integer`, f.pos);
      return Math.trunc(Number((v as string).trim())) || 0;
    }
    case 'json':
      return canonicalize(v);
    case 'sha256':
      t(isStrNum(v));
      return toHex(sha256(utf8(scalarText(v)!)));
    case 'sha512':
      t(isStrNum(v));
      return toHex(sha512(utf8(scalarText(v)!)));
    case 'mosquitto_passwd': {
      t(isStrNum(v));
      const pw = scalarText(v)!;
      if (pw === '') throw new ExprError('E_TYPE', 'filter "mosquitto_passwd" cannot hash an empty password', f.pos);
      if (pw.includes('\u0000')) throw new ExprError('E_TYPE', 'filter "mosquitto_passwd" cannot hash a password with a NUL character', f.pos);
      const format = (f.args[0] ?? 7) as 6 | 7;
      const iterations = (f.args[1] as number | undefined) ?? MOSQUITTO_DEFAULT_ITERATIONS;
      return mosquittoHash(pw, mosquittoSalt(pw, ctx.deployment.id, ctx.self.slug), format, iterations);
    }
  }
}

function toText(v: ExprValue, pos: number): string {
  if (v === null || v === undefined) throw new ExprError('E_NULL', 'the value is null; add | default(…)', pos);
  if (Array.isArray(v) || typeof v === 'object') throw new ExprError('E_TYPE', 'a list or an object needs | join(…) or | json', pos);
  return scalarText(v)!;
}

/**
 * Resolve one value for one field. Throws ExprError.
 * - text fields: returns the text (plain values without "{{" are returned unchanged)
 * - numeric fields (hostPort, imagePort): the value must be ONE whole block that resolves to an integer 1–65535
 */
export function resolve(src: string, ctx: ExprContext, field: ExprField, hooks?: ResolveHooks): ResolveResult {
  const segs = parseTemplate(src);
  const numeric = NUMERIC_FIELDS.includes(field);
  if (numeric) checkNumericShape(segs);
  let out = '';
  let masked = '';
  let secret = false;
  const refs: string[] = [];
  let deferred = false;
  let clearSecret = false; // §21 #103: a secret reached the result without a password hash
  for (const s of segs) {
    if (s.kind === 'text') {
      if (!numeric) {
        out += s.text;
        masked += s.text;
      }
      continue;
    }
    let v: ExprValue;
    let isSecret = false;
    let blockDeferred = false;
    if (s.primary.kind === 'lit') v = s.primary.value;
    else {
      const r = lookup(s.primary, ctx, field, hooks);
      v = r.v;
      isSecret = r.secret;
      blockDeferred = isDeferredValue(v);
      refs.push(s.primary.segments.join('.'));
    }
    for (const f of s.filters) checkFilterAllowed(f, field); // §21 #103 (never deferred away)
    if (isSecret && !s.filters.some((f) => HASHING_FILTERS.includes(f.name))) clearSecret = true;
    let txt: string;
    try {
      for (const f of s.filters) v = applyFilter(f, v, ctx);
      txt = toText(v, s.start);
    } catch (e) {
      // a filter / type error caused by the stand-in text is not a real problem: the value comes at deployment
      if (blockDeferred && e instanceof ExprError) throw new ExprError('E_DEFERRED', `${refs[refs.length - 1]} is set at deployment`, e.pos);
      throw e;
    }
    if (blockDeferred && !isDeferredValue(txt)) txt = DEPLOY_PLACEHOLDER; // e.g. | length, | int of the stand-in
    if (blockDeferred) deferred = true;
    out += txt;
    masked += isSecret ? '••••' : txt;
    if (isSecret) secret = true;
  }
  if (numeric && deferred) throw new ExprError('E_DEFERRED', 'the number is set at deployment', 0);
  if (numeric) {
    const t = out.trim();
    const n = /^-?\d+(\.\d+)?$/.test(t) ? Number(t) : NaN;
    if (!Number.isInteger(n) || n < 1 || n > 65535) throw new ExprError('E_TYPE', `"${out}" is not an integer 1–65535`, 0);
    return { value: formatNumber(n), number: n, masked: secret ? '••••' : formatNumber(n), secret, refs };
  }
  const r: ResolveResult = deferred ? { value: out, number: null, masked, secret, refs, deferred } : { value: out, number: null, masked, secret, refs };
  if (secret && !clearSecret) r.secretHashed = true;
  return r;
}

/**
 * Resolve a number-or-expression field (Container.port[].hostPort, ImagePort.port).
 * number → checked 1–65535; string → must be one whole block; null → null.
 */
export function resolveNumberField(v: number | string | null, ctx: ExprContext, field: 'hostPort' | 'imagePort'): number | null {
  if (v === null) return null;
  if (typeof v === 'number') {
    if (!Number.isInteger(v) || v < 1 || v > 65535) throw new ExprError('E_TYPE', `${v} is not an integer 1–65535`, 0);
    return v;
  }
  if (!isExpr(v)) throw new ExprError('E_TYPE', 'a number field must be a number or ONE whole block, for example {{ var.api-port }}', 0);
  return resolve(v, ctx, field).number;
}

/** Convenience: run resolve() and return either the result or the error (never throws ExprError). */
export function tryResolve(src: string, ctx: ExprContext, field: ExprField): { ok: true; result: ResolveResult } | { ok: false; error: ExprError } {
  try {
    return { ok: true, result: resolve(src, ctx, field) };
  } catch (e) {
    if (e instanceof ExprError) return { ok: false, error: e };
    throw e;
  }
}

// ───────────────────────────────────────────────────────────── one container: env + mount paths in dependency order
/** One env var of a container: the container's own item (field env) or the image default (field imageEnv). */
export interface EnvSource {
  name: string;
  /** the value (may hold {{ }}); null = no value (image env without default, or a secretRef) */
  src: string | null;
  field: 'env' | 'imageEnv';
  /** secretRef items: the plain secret value (null = the secret has no value); src must be null */
  secretRef?: { value: string | null } | null;
}
/** One mount of a container (explicit volume or implicit image volume). key: e.g. "volume[0]" or "image:<name>". */
export interface MountSource {
  key: string;
  src: string;
  /** file volumes: the fileSlug (file.<slug>.path answers this mount's resolved path) */
  fileSlug?: string | null;
}
export type Resolved<T> = { ok: true; result: T } | { ok: false; error: ExprError };
export interface ContainerResolution {
  env: Record<string, Resolved<ResolveResult>>;
  mounts: Record<string, Resolved<ResolveResult>>;
}

/**
 * Resolve every env var and mount path of ONE container (SPEC §10.5). Order: lazily, in dependency order —
 * `container.env.X` resolves X first, `file.<slug>.path` resolves that file's mount first. A cycle gives E_CYCLE
 * (message = the chain) for every value on it; a value that uses a failed value fails with the same code at its
 * own reference. base.file is ignored for file slugs that this container mounts (their resolved mount path is used);
 * slugs it does not mount keep base.file (path null → E_FILE_NOT_MOUNTED).
 * Never throws ExprError.
 */
export function resolveContainer(base: ExprContext, env: EnvSource[], mounts: MountSource[]): ContainerResolution {
  const envBy = new Map(env.map((e) => [e.name, e]));
  const mountBy = new Map(mounts.map((m) => [m.key, m]));
  const fileMount = new Map<string, string>();
  for (const m of mounts) if (m.fileSlug && !fileMount.has(m.fileSlug)) fileMount.set(m.fileSlug, m.key);
  const out: ContainerResolution = { env: {}, mounts: {} };
  const stack: string[] = []; // node ids "env:NAME" / "mount:KEY"
  const label = (id: string) => (id.startsWith('env:') ? `container.env.${id.slice(4)}` : `mount ${id.slice(6)}`);
  const ctx: ExprContext = { ...base, container: base.container ? { ...base.container, env: undefined } : base.container };

  const dep = (id: string, p: PathNode): ResolveResult => {
    if (stack.includes(id)) {
      const chain = [...stack.slice(stack.indexOf(id)), id].map(label).join(' → ');
      throw new ExprError('E_CYCLE', `cycle: ${chain}`, p.pos);
    }
    const r = node(id);
    if (!r.ok) throw new ExprError(r.error.code, `${label(id)}: ${r.error.message}`, p.pos);
    return r.result;
  };
  const hooks: ResolveHooks = {
    env: (name, p) => {
      if (!envBy.has(name)) return undefined;
      const r = dep(`env:${name}`, p);
      const e = envBy.get(name)!;
      const value = e.secretRef ? e.secretRef.value : e.src === null ? null : r.value;
      return { value, secret: r.secret };
    },
    file: (slug, p) => {
      const key = fileMount.get(slug);
      if (key === undefined) return base.file && Object.prototype.hasOwnProperty.call(base.file, slug) ? base.file[slug] : undefined;
      return { path: dep(`mount:${key}`, p).value };
    },
  };
  const node = (id: string): Resolved<ResolveResult> => {
    const isEnv = id.startsWith('env:');
    const key = id.slice(isEnv ? 4 : 6);
    const memo = isEnv ? out.env[key] : out.mounts[key];
    if (memo) return memo;
    stack.push(id);
    let r: Resolved<ResolveResult>;
    try {
      if (isEnv) {
        const e = envBy.get(key)!;
        if (e.secretRef) r = { ok: true, result: { value: e.secretRef.value ?? '', number: null, masked: '••••', secret: true, refs: [] } };
        else if (e.src === null) r = { ok: true, result: { value: '', number: null, masked: '', secret: false, refs: [] } };
        else r = { ok: true, result: resolve(e.src, ctx, e.field, hooks) };
      } else r = { ok: true, result: resolve(mountBy.get(key)!.src, ctx, 'mountPath', hooks) };
    } catch (e) {
      if (!(e instanceof ExprError)) throw e;
      r = { ok: false, error: e };
    }
    stack.pop();
    if (isEnv) out.env[key] = r;
    else out.mounts[key] = r;
    return r;
  };
  for (const e of env) node(`env:${e.name}`);
  for (const m of mounts) node(`mount:${m.key}`);
  return out;
}
