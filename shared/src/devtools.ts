/**
 * Dev tools (SPEC-final §7.6, §21 #97): service GUIs — HTTP client (Postman-like), PostgreSQL, SQLite, Cassandra (CQL),
 * Redis and MQTT — whose queries the AGENT runs on the site server; the agent GUI is only the workbench.
 *
 * Every path is per site: /_opm/api/sites/{siteSlug}/devtools/… . {siteSlug} = this site, or another site (forwarded over
 * the mesh with the actor identity; SSE copied; both sites write an activity). "*" is not supported (a connection lives
 * on one site): the GUI asks the operator to pick a site.
 *
 * Connections:
 *   - presets  = SiteConfig.devTools of the head revision (cloud: template + deployment editors, rides in the revision);
 *                read-only on the agent ("Duplicate to edit locally"); passwords as `{{ secret.x }}` / `{{ var.y }}` /
 *                container env, or (§21 #103) a static value stored in the config (masked in agent reads, Reveal).
 *   - manual   = created in the agent GUI, stored per site in the agent SQLite (`devtools_connections`); literal
 *                passwords are stored like secrets (masked in every read, RevealValue 60 s, logged).
 *   The agent keeps ONE pooled client per connection (remembered: connect once, reuse; idle timeout; reconnect on error).
 *
 * Auth: reads = session cookie; EVERY execution / write / reveal = session + actor (activities carry the actor).
 * Streams (MQTT messages) = text/event-stream opened with a single-use ticket from an actor POST.
 * Every execution writes `devtools.query` (redacted statement, ≤ 4 KiB, NO result data).
 */
import { createClient, ep, type Client } from './http.js';
import type { ListResponse, Masked, PageResponse, RevealResponse, Slug } from './types.js';

const S = 'session' as const;
const SA = 'session+actor' as const;

// ───────────────────────────────────────────────────────────── kinds + connection shape

export const DEVTOOL_KINDS = ['http', 'postgres', 'sqlite', 'cassandra', 'redis', 'mqtt'] as const;
export type DevToolKind = (typeof DEVTOOL_KINDS)[number];
export const DEVTOOL_KIND_LABEL: Record<DevToolKind, string> = {
  http: 'HTTP client',
  postgres: 'PostgreSQL',
  sqlite: 'SQLite',
  cassandra: 'Cassandra (CQL)',
  redis: 'Redis',
  mqtt: 'MQTT',
};
/** Default container / host port per kind (the GUIs pre-fill it; http: 80, https 443). */
export const DEVTOOL_DEFAULT_PORT: Record<Exclude<DevToolKind, 'sqlite'>, number> = { http: 80, postgres: 5432, cassandra: 9042, redis: 6379, mqtt: 1883 };

/**
 * Where the agent connects to.
 *   container      — a container of THIS site + one of its image port names. The agent dials the container's IP on the
 *                    site network (docker inspect) and falls back to the published loopback / host port (127.0.0.1:<hostPort>)
 *                    when the IP is not routable from the agent (Docker Desktop / Colima on macOS).
 *   host           — any host:port reachable from the site server (IP or DNS name; no {{ }}).
 *   url            — http only: an absolute base URL (http / https; may hold `{{ var.x }}`).
 *   volume-file    — sqlite only: a file inside a declared site volume (relative path, no "..").
 *   container-file — sqlite only: an absolute path inside a container; the agent maps it through the container's mounts to
 *                    the host file (volume / host bind); a path that is not on a mount → 422 (container-local storage
 *                    cannot be opened from outside).
 */
export type DevToolTarget =
  | { kind: 'container'; containerSlug: Slug; portName: Slug }
  | { kind: 'host'; host: string; port: number }
  | { kind: 'url'; url: string }
  | { kind: 'volume-file'; volumeSlug: string; path: string } //   the file must exist; a DIR volume only; never through a symlink
  | { kind: 'container-file'; containerSlug: Slug; path: string };
export type DevToolTargetKind = DevToolTarget['kind'];
/** Allowed target kinds per connection kind. */
export const DEVTOOL_TARGETS: Record<DevToolKind, readonly DevToolTargetKind[]> = {
  http: ['container', 'host', 'url'],
  postgres: ['container', 'host'],
  cassandra: ['container', 'host'],
  redis: ['container', 'host'],
  mqtt: ['container', 'host'],
  sqlite: ['volume-file', 'container-file'],
};

/**
 * A credential: none, an expression resolved ON THE SITE from the head config (`{{ secret.pg-password }}`,
 * `{{ var.db_user }}`; roots var, secret, site, self, deployment, containers), or a literal = a static value: manual
 * connections store it on the agent; presets (§21 #103, the "Value" mode) store it in the config in plain text (warning
 * V_DEVTOOL_LITERAL). Agent reads return MASK either way, Reveal returns the value; the cloud Diff shows "••••".
 */
export type DevToolCredential =
  | { kind: 'none' }
  | { kind: 'expr'; expr: string }
  | { kind: 'literal'; value: string | Masked };

export type DevToolHttpAuthScheme = 'none' | 'basic' | 'bearer' | 'api-key';
/**
 * Auth fields. Which ones apply depends on the kind (unused ones are null / none; the validator says so):
 *   postgres  username (required), password, database (default = username)
 *   cassandra username + password (PasswordAuthenticator; both or none), database = default keyspace (optional)
 *   redis     username (ACL, optional), password (optional), database = db index "0"–"15" (default "0")
 *   mqtt      username, password, clientId (null = "opm-devtools-<site>-<random>")
 *   http      httpScheme + username / password (basic) | token (bearer / api-key value) + apiKeyHeader (api-key)
 *   sqlite    nothing
 * username / database / clientId / apiKeyHeader may hold {{ }} (var.*, secret.*, site.*, self.*, deployment.*, containers.*).
 */
export interface DevToolAuth {
  username: string | null;
  password: DevToolCredential;
  database: string | null;
  clientId: string | null;
  httpScheme: DevToolHttpAuthScheme;
  token: DevToolCredential;
  apiKeyHeader: string | null; // default "X-API-Key"
}

export type PgSslMode = 'disable' | 'prefer' | 'require' | 'verify-full';
export type CqlConsistency = 'ANY' | 'ONE' | 'TWO' | 'THREE' | 'QUORUM' | 'ALL' | 'LOCAL_QUORUM' | 'EACH_QUORUM' | 'LOCAL_ONE';
/** Options (all kinds share one flat shape; null = the default shown). */
export interface DevToolOptions {
  /** TLS to the service (pg: sslMode wins; redis / mqtt / cassandra / http https). */
  tls: boolean; //                    default false (http: from the URL scheme)
  tlsInsecure: boolean; //            default false: skip certificate verification
  tlsServerName: string | null;
  sslMode: PgSslMode | null; //        postgres; default 'prefer'
  /** statement / command / request timeout, 1–3600 s (default 30; agent.json devTools.statementTimeoutSec) */
  timeoutSec: number | null;
  /** rows per page returned by execute, 1–10000 (default 1000; agent.json devTools.maxRows) */
  maxRows: number | null;
  consistency: CqlConsistency | null; // cassandra; default LOCAL_ONE
  // http
  scheme: 'http' | 'https' | null; //  container / host targets: default http
  basePath: string | null; //          prefix for relative request URLs, e.g. "/api"
  followRedirects: boolean | null; //  default true
  defaultHeaders: { name: string; value: string }[]; // added to every request (value may hold {{ }})
  // mqtt
  cleanSession: boolean | null; //     default true
  keepAliveSec: number | null; //      default 30
}

/**
 * A preset in the cloud config: SiteConfig.devTools[] (templates + deployments; rides in the revision). Same shape as a
 * connection. password / token: 'none' | 'expr' (normally a secret ref, which keeps the value out of the config) | since
 * §21 #103 'literal' (a static value, stored in plain text: warning V_DEVTOOL_LITERAL; agents ≥ DEVTOOL_LITERAL_MIN_AGENT).
 * `presetSlug` is unique per site (RE.devToolSlug).
 */
export interface DevToolPreset {
  presetSlug: string; //  ^[a-z0-9][a-z0-9-]{0,62}$
  name: string; //        1–80
  description: string; // 0–2000
  kind: DevToolKind;
  target: DevToolTarget;
  auth: DevToolAuth;
  options: DevToolOptions;
  readOnly: boolean; //   default false
}
export const DEVTOOL_SLUG_RE = /^[a-z0-9][a-z0-9-]{0,62}$/;
export const DEVTOOL_PRESETS_MAX = 50;

/** A connection as the agent API returns it (presets + manual). Literal credentials are always MASK. */
export interface DevToolConnection {
  /** "p:<presetSlug>" for presets; "c_<ULID>" for manual connections */
  id: string;
  siteSlug: Slug;
  source: 'preset' | 'manual';
  /** presets: the head revision seq that defines it */
  presetRevSeq: number | null;
  name: string;
  description: string;
  kind: DevToolKind;
  target: DevToolTarget;
  auth: DevToolAuth;
  options: DevToolOptions;
  readOnly: boolean;
  createdBy: string | null; // actor e-mail (manual)
  createdAt: string | null;
  updatedAt: string | null;
  /** human address the agent resolved last ("postgres → 172.18.0.4:5432", "app.db (volume data)") */
  resolvedTarget: string | null;
  /** pooled client state ("remembered") */
  state: DevToolConnState;
  /** the preset references something the head does not have (deleted container / port / secret); null = ok */
  problem: string | null;
}
export interface DevToolConnState {
  status: 'idle' | 'connected' | 'error';
  connectedAt: string | null;
  lastUsedAt: string | null;
  lastError: string | null;
  serverVersion: string | null;
  runningQueries: number;
}

/** Create / update a manual connection. password / token literal: value = MASK keeps the stored value. */
export interface DevToolConnectionInput {
  name: string;
  description: string;
  kind: DevToolKind;
  target: DevToolTarget;
  auth: DevToolAuth;
  options: DevToolOptions;
  readOnly: boolean;
}
export interface DuplicateConnectionRequest {
  name?: string;
}
export interface TestConnectionResponse {
  ok: boolean;
  latencyMs: number;
  serverVersion: string | null;
  resolvedTarget: string | null;
  message: string; // "connected" / the driver error (secrets masked)
}

// ───────────────────────────────────────────────────────────── agent.json + info

/** agent.json `tools.devTools` (switch, default true) and `devTools` (limits; all optional). */
export interface AgentDevToolsConfig {
  tools?: { devTools?: boolean /* true */ };
  devTools?: {
    statementTimeoutSec?: number; // 30 (1–3600)
    maxRows?: number; //           1000 rows per page (1–10000)
    bufferRows?: number; //        10000 rows kept per result for paging / export (≤ 100000)
    maxCellBytes?: number; //      65536 bytes of one value inline; bigger → truncated + "download full value"
    maxResultBytes?: number; //    67108864 bytes buffered per result
    maxResponseBytes?: number; //  10485760 bytes of an HTTP response body (inline ≤ 2 MiB, the rest via download)
    idleTimeoutSec?: number; //    600: pooled clients closed after this idle time
    maxConcurrent?: number; //     8 running executions per site
  };
}
export interface DevToolsLimits {
  statementTimeoutSec: number;
  maxRows: number;
  bufferRows: number;
  maxCellBytes: number;
  maxResultBytes: number;
  maxResponseBytes: number;
  inlineResponseBytes: number;
  idleTimeoutSec: number;
  maxConcurrent: number;
  executePerMinute: number;
  revealPerMinute: number;
  mqttStreamMaxSec: number;
  historyDays: number;
}
export const DEVTOOLS_DEFAULT_LIMITS: DevToolsLimits = {
  statementTimeoutSec: 30,
  maxRows: 1000,
  bufferRows: 10000,
  maxCellBytes: 65536,
  maxResultBytes: 64 * 1024 * 1024,
  maxResponseBytes: 10 * 1024 * 1024,
  inlineResponseBytes: 2 * 1024 * 1024,
  idleTimeoutSec: 600,
  maxConcurrent: 8,
  executePerMinute: 120,
  revealPerMinute: 30,
  mqttStreamMaxSec: 6 * 3600,
  historyDays: 30,
};
export interface DevToolsInfo {
  siteSlug: Slug;
  enabled: boolean;
  /** why it is off (agent.json `tools.devTools: false`) */
  reason: string | null;
  configPath: string | null;
  limits: DevToolsLimits;
  kinds: DevToolKind[];
  /** containers + ports + volumes of the head (for the manual connection form pickers) */
  containers: {
    containerSlug: Slug;
    serviceSlug: Slug;
    image: string;
    running: boolean;
    ports: { portName: Slug; containerPort: number; protocol: string; hostPort: number | null }[];
    mounts: { mountPath: string; kind: string; volumeSlug: string | null }[];
    /** §21 #99 (agent ≥ 0.11.1): env names (image env + container env) + secret flag for containers.<c>.env.<NAME> — never values */
    env?: { name: string; secret: boolean }[];
  }[];
  volumes: { volumeSlug: string; label: string; kind: 'dir' | 'file' }[];
  secrets: Slug[]; // slugs only
  variables: Slug[];
}

// ───────────────────────────────────────────────────────────── schema introspection

export interface SchemaColumn {
  name: string;
  type: string;
  nullable: boolean;
  default: string | null;
  primaryKey: boolean;
  /** cassandra: partition_key | clustering | regular | static */
  role?: string | null;
  comment?: string | null;
}
export interface SchemaIndex {
  name: string;
  definition: string;
  unique: boolean;
  primary: boolean;
}
export interface SchemaTable {
  name: string;
  kind: 'table' | 'view' | 'materialized-view' | 'foreign-table' | 'partitioned-table';
  /** pg: reltuples estimate; sqlite: null (count on demand); cassandra: null */
  rowEstimate: number | null;
  sizeBytes: number | null;
  columns: SchemaColumn[];
  comment: string | null;
}
export interface SchemaFunction {
  name: string;
  kind: 'function' | 'procedure' | 'aggregate' | 'trigger';
  arguments: string;
  returns: string;
}
/** One namespace: pg schema, cassandra keyspace, sqlite "main" (+ attached). */
export interface SchemaNamespace {
  name: string;
  system: boolean; // pg_catalog / information_schema / system* keyspaces (the GUI folds them)
  tables: SchemaTable[];
  functions: SchemaFunction[];
  /** cassandra: replication map; pg: owner */
  meta: Record<string, string>;
}
export interface SqlSchemaResponse {
  kind: 'postgres' | 'sqlite' | 'cassandra';
  serverVersion: string | null;
  database: string | null; //  pg current_database(); cassandra current keyspace; sqlite file path
  namespaces: SchemaNamespace[];
  loadedAt: string;
  /** introspection stopped at 2000 tables / 20000 columns */
  truncated: boolean;
}
/** Detail of one table: indexes, DDL, constraints (loaded when the operator opens it). */
export interface TableDetailResponse {
  namespace: string;
  table: string;
  columns: SchemaColumn[];
  indexes: SchemaIndex[];
  constraints: { name: string; type: string; definition: string }[];
  ddl: string | null;
  rowEstimate: number | null;
  sizeBytes: number | null;
}

// ───────────────────────────────────────────────────────────── execute (SQL / CQL / Redis / HTTP)

/**
 * A value cell in a result. Large values are cut at maxCellBytes (truncated) — fetch the full value by ref.
 * Encodings: bytes (bytea / blob) = "\\x" + lowercase hex (pg text form); timestamps RFC 3339; integers beyond 2^53 as
 * decimal strings; json / jsonb / maps / lists / UDTs as JSON values.
 */
export type CellValue = string | number | boolean | null | CellTruncated | Record<string, unknown> | unknown[];
export interface CellTruncated {
  $truncated: true;
  preview: string;
  sizeBytes: number;
  /** false = not downloadable by resultId (e.g. a Redis reply — use the key browser); absent = true */
  fetchable?: boolean;
  /** GET …/results/{resultId}/value?row&col (raw bytes, attachment) */
  row: number;
  col: number;
}
export interface ResultColumn {
  name: string;
  /** driver type name: int4, text, jsonb, timestamptz, uuid, blob, varchar, map<text,int> … */
  type: string;
  /** GUI renderer hint */
  display: 'number' | 'text' | 'bool' | 'json' | 'datetime' | 'bytes' | 'uuid' | 'other';
}
/** One statement's outcome inside an execution. */
export interface StatementResult {
  index: number;
  /** the statement text (as run, ≤ 4 KiB) */
  statement: string;
  ok: boolean;
  /** position: 1-based character offset in THIS statement's text as the operator wrote it (an EXPLAIN prefix added by
   *  the agent is subtracted); null = unknown */
  error: { message: string; code: string | null; position: number | null; detail: string | null; hint: string | null } | null;
  /** 'rows' = a result set; 'command' = DDL / DML tag; 'explain' = plan */
  type: 'rows' | 'command' | 'explain';
  /** "SELECT 3", "INSERT 0 1", "UPDATE 7", "CREATE TABLE" */
  commandTag: string | null;
  rowsAffected: number | null;
  columns: ResultColumn[];
  /** first page (≤ maxRows) */
  rows: CellValue[][];
  /** rows held by the agent for paging / export (≤ bufferRows) */
  totalRows: number;
  /** more rows existed beyond bufferRows / maxResultBytes (not fetched) */
  truncated: boolean;
  /** paging + export handle (kept 15 min, per session) */
  resultId: string | null;
  durationMs: number;
  /** explain: raw text plan + (pg FORMAT JSON) parsed plan */
  plan?: { text: string; json: unknown | null } | null;
  notices?: string[];
}
export interface ExecuteSqlRequest {
  /** one or more statements; split on ";" outside quotes / comments / dollar quotes (splitSqlStatements) */
  statement: string;
  /** positional parameters $1… (pg) / ? (sqlite, cql); JSON values */
  params?: unknown[];
  /** EXPLAIN (pg: EXPLAIN (FORMAT JSON[, ANALYZE]); sqlite: EXPLAIN QUERY PLAN; cassandra: refused) */
  explain?: 'plan' | 'analyze' | null;
  /** client-chosen id (ULID) so the operator can cancel it */
  queryId: string;
  timeoutSec?: number;
  maxRows?: number;
  /** destructive statements: the typed connection name (see classifyStatement) */
  confirm?: string;
  /** run all statements in one transaction (pg / sqlite; default false = autocommit each) */
  transaction?: boolean;
  /** cassandra: page state / consistency override */
  consistency?: CqlConsistency;
}
export interface ExecuteResponse {
  queryId: string;
  connectionId: string;
  ok: boolean;
  durationMs: number;
  results: StatementResult[];
  /** activity id of the devtools.query entry */
  activityId: string | null;
}
export interface ResultPageResponse {
  resultId: string;
  offset: number;
  rows: CellValue[][];
  totalRows: number;
  truncated: boolean;
}

// Redis
export interface ExecuteRedisRequest {
  /** argv, e.g. ["HGETALL", "user:1"]; or `line` parsed with shell-like quoting (parseRedisCommandLine) */
  command?: string[];
  line?: string;
  queryId: string;
  timeoutSec?: number;
  confirm?: string;
}
/** Agent behaviour: `map` = RedisReply[] alternating key, value (keys sorted); RESP3 sets come as `array`; status replies as
 *  `string`; invalid UTF-8 → U+FFFD; an oversized value is CellTruncated with row/col 0 (not fetchable — use the key browser). */
export interface RedisReply {
  /** RESP type: string | integer | array | nil | error | double | bool (map → alternating array, see above) */
  type: string;
  value: CellValue | RedisReply[];
}
export interface ExecuteRedisResponse {
  queryId: string;
  ok: boolean;
  durationMs: number;
  command: string; // redacted argv joined (AUTH / CONFIG SET requirepass … masked)
  reply: RedisReply | null;
  error: string | null;
  activityId: string | null;
}
export interface RedisKeyRow {
  key: string;
  type: 'string' | 'hash' | 'list' | 'set' | 'zset' | 'stream' | 'ReJSON-RL' | string;
  ttlSec: number; // -1 = no expiry, -2 = gone
  sizeBytes: number | null; // MEMORY USAGE (null when the command is not allowed)
  length: number | null; //   STRLEN / HLEN / LLEN / SCARD / ZCARD / XLEN
}
export interface RedisScanResponse {
  cursor: string; // "0" = done
  keys: RedisKeyRow[];
  scanned: number;
  dbSize: number;
}
export interface RedisKeyValueResponse {
  key: string;
  type: string;
  ttlSec: number;
  length: number | null;
  /** string: CellValue; hash: [field, value][]; list: values; set: members; zset: [member, score][]; stream: [{ id, fields }] */
  value: unknown;
  /** next page: a number offset (list / zset), the SCAN cursor (hash / set), the last id (stream); null = done.
   *  Pass it back as the `cursor` query (String(next)). */
  next: string | number | null;
  truncated: boolean;
  encoding: string | null;
}
/** Typed shapes of RedisKeyValueResponse.value per type. */
export type RedisHashValue = [field: string, value: CellValue][];
export type RedisZsetValue = [member: string, score: number][];
export type RedisStreamValue = { id: string; fields: Record<string, CellValue> }[];
export type RedisListValue = CellValue[]; // list + set

export interface RedisInfoResponse {
  serverVersion: string | null;
  sections: Record<string, Record<string, string>>; // INFO parsed
  dbs: { db: number; keys: number; expires: number }[];
  slowlog: { id: number; at: string; durationUs: number; command: string }[];
}

// MQTT
export type MqttPayloadEncoding = 'utf8' | 'json' | 'base64' | 'hex';
export interface MqttPublishRequest {
  topic: string;
  payload: string;
  encoding: MqttPayloadEncoding; // how `payload` is written (json = utf8 that must parse)
  qos: 0 | 1 | 2;
  retain: boolean;
  confirm?: string; // publishing an EMPTY retained payload clears a retained message → typed connection name
}
export interface MqttPublishResponse {
  ok: boolean;
  durationMs: number;
  bytes: number;
  activityId: string | null;
}
export interface MqttSubscription {
  filter: string;
  qos: 0 | 1 | 2;
}
export interface MqttSessionRequest {
  subscriptions: MqttSubscription[];
}
export interface MqttSessionResponse {
  sessionId: string;
  /** single-use, 60 s (the session is closed when its stream is not opened within 2 min) */
  ticket: string;
  /** absolute path INCLUDING `?ticket=…` — open it as is with EventSource */
  streamPath: string;
  expiresAt: string;
}
export interface MqttMessage {
  seq: number;
  at: string;
  topic: string;
  qos: number;
  retained: boolean;
  duplicate: boolean;
  sizeBytes: number;
  /** utf8 text when valid UTF-8 (≤ maxCellBytes), else null + base64 */
  text: string | null;
  base64: string | null;
  truncated: boolean;
}
/** SSE events of the MQTT stream: `meta` {sessionId, subscriptions}, `message` MqttMessage, `status` {connected, error?},
 *  `dropped` {count} (slow consumer), `error` {message}, `end` {reason}; ": ping" every 15 s. */
export type MqttStreamEvent =
  | { event: 'meta'; data: { sessionId: string; subscriptions: MqttSubscription[] } }
  | { event: 'message'; data: MqttMessage }
  | { event: 'status'; data: { connected: boolean; error: string | null } }
  | { event: 'dropped'; data: { count: number } }
  | { event: 'error'; data: { message: string } }
  | { event: 'end'; data: { reason: string } };
export interface MqttTopicSeen {
  topic: string;
  messages: number;
  lastAt: string;
  retained: boolean;
  lastPreview: string | null;
  lastSizeBytes: number;
}

// HTTP
export type HttpBody =
  | { kind: 'none' }
  | { kind: 'json'; text: string }
  | { kind: 'text'; text: string; contentType: string | null }
  | { kind: 'form'; fields: { name: string; value: string; enabled: boolean }[] } // x-www-form-urlencoded
  | { kind: 'multipart'; fields: { name: string; value: string; enabled: boolean; fileName: string | null; base64: boolean }[] }
  | { kind: 'binary'; base64: string; contentType: string | null };
export interface HttpKV {
  name: string;
  value: string;
  enabled: boolean;
}
/**
 * One request. url: absolute (http/https) or relative to the connection base (container / host / url target +
 * basePath). Placeholders: `{{name}}` = an environment variable (the selected environment), `{{ secret.x }}` /
 * `{{ var.y }}` / … = OPM expressions resolved on the site. The agent never sends the browser's cookies; a Cookie
 * header naming opm_session* is dropped; paths /_opm/api/* and /_opm/peer/* are refused (the agent's own API).
 */
export interface HttpRequestSpec {
  method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE' | 'HEAD' | 'OPTIONS';
  url: string;
  query: HttpKV[];
  headers: HttpKV[];
  body: HttpBody;
  /** null = the connection auth */
  auth: { scheme: DevToolHttpAuthScheme; username: string | null; password: string | null; token: string | null; apiKeyHeader: string | null; apiKeyIn: 'header' | 'query' } | null;
  followRedirects: boolean | null;
  tlsInsecure: boolean | null;
  timeoutSec: number | null;
}
export interface ExecuteHttpRequest {
  request: HttpRequestSpec;
  /** an environment id (agent-stored) or inline variables (the inline ones win) */
  environmentId?: string | null;
  variables?: Record<string, string>;
  queryId: string;
  confirm?: string;
}
export interface HttpTiming {
  dnsMs: number | null;
  connectMs: number | null;
  tlsMs: number | null;
  ttfbMs: number | null;
  downloadMs: number | null;
  totalMs: number;
}
export interface ExecuteHttpResponse {
  queryId: string;
  ok: boolean; //       transport ok (any HTTP status)
  error: string | null;
  status: number | null;
  statusText: string | null;
  protocol: string | null; // HTTP/1.1 | HTTP/2.0
  finalUrl: string | null; // after redirects (secrets masked)
  redirects: { status: number; url: string }[];
  headers: [string, string][];
  cookies: { name: string; value: string; domain: string | null; path: string | null; expires: string | null; httpOnly: boolean; secure: boolean }[];
  contentType: string | null;
  sizeBytes: number; //  body bytes received (≤ maxResponseBytes)
  headerBytes: number;
  /** body ≤ inlineResponseBytes: text when text-like valid UTF-8, else base64. Body > inline: BOTH null (nothing
   *  inline) — download it with resultBody. bodyTruncated = the body was cut at maxResponseBytes. */
  bodyText: string | null;
  bodyBase64: string | null;
  bodyTruncated: boolean;
  /** download the full body: GET …/results/{resultId}/body */
  resultId: string | null;
  timing: HttpTiming;
  /** the request as sent (placeholders resolved, secrets masked) */
  sent: { method: string; url: string; headers: [string, string][]; bodyBytes: number };
  activityId: string | null;
}

export interface CancelRequest {
  queryId: string;
}
export interface CancelResponse {
  cancelled: boolean; // false = not running (already finished)
}

// ───────────────────────────────────────────────────────────── saved items, environments, history

/** A saved query / request ("collections"). Per site; visible to every operator (createdBy shown). */
export interface DevToolSaved {
  id: string; //             "s_<ULID>"
  kind: DevToolKind;
  connectionId: string | null; // null = any connection of the kind
  collection: string; //      folder name ("" = root), ≤ 80
  name: string; //            1–120
  description: string;
  /** sql / cql / redis line / mqtt topic+payload JSON / http HttpRequestSpec JSON */
  content: string;
  createdBy: string | null;
  createdAt: string;
  updatedAt: string;
}
export type DevToolSavedInput = Pick<DevToolSaved, 'kind' | 'connectionId' | 'collection' | 'name' | 'description' | 'content'>;
/** HTTP environments (Postman-like); secret values are stored like literal passwords (masked, reveal). */
export interface DevToolEnvironment {
  id: string; // "e_<ULID>"
  name: string;
  variables: { name: string; value: string | Masked; secret: boolean; enabled: boolean }[];
  createdBy: string | null;
  updatedAt: string;
}
export type DevToolEnvironmentInput = { name: string; variables: { name: string; value: string; secret: boolean; enabled: boolean }[] };
/** One history row = one devtools.query activity (redacted statement; no result data). */
export interface DevToolHistoryItem {
  activityId: string;
  ts: string;
  actor: string | null;
  connectionId: string;
  connectionName: string;
  kind: DevToolKind;
  op: DevToolOp;
  statement: string; // redacted, ≤ 4 KiB
  ok: boolean;
  durationMs: number;
  rows: number | null;
  status: number | null;
  error: string | null;
  via: string | null;
}
/** history rows also cover MQTT publishes (op mqtt-publish, statement "PUBLISH <topic> qos N …") */
export type DevToolOp = 'sql' | 'explain' | 'cql' | 'redis' | 'http' | 'mqtt-publish' | 'mqtt-subscribe' | 'introspect';

/** Activity data of devtools.query (Appendix B). */
export interface DevToolQueryActivity {
  siteSlug: Slug;
  connectionId: string;
  connectionName: string;
  kind: DevToolKind;
  source: 'preset' | 'manual';
  target: string; //       resolved target text
  op: DevToolOp;
  statement: string; //    redacted, ≤ 4 KiB (STATEMENT_LOG_MAX)
  statementTruncated: boolean;
  statements: number; //   count
  durationMs: number;
  rows: number | null;
  rowsAffected: number | null;
  status: number | null; // http
  ok: boolean;
  error: string | null; // ≤ 500 chars, secrets masked
  readOnly: boolean;
  destructive: boolean;
  confirmed: boolean;
  queryId: string;
  via?: string; //         forwarding site
}
export const STATEMENT_LOG_MAX = 4096;

/** `details` of dev-tools errors (ApiErrorBody.error.details). */
export interface DevToolConfirmDetails { confirmRequired: true; reasons: string[] } //   422 invalid: destructive, retry with confirm
export interface DevToolBlockedDetails { blocked: true; command: string } //             422 invalid: blocked Redis command
export interface DevToolDisabledDetails { reason: string; setting: 'tools.devTools' } // 403 forbidden: switched off
export interface DevToolExprDetails { expression: string; code: string; pos: number | null } // 422 invalid: credential / URL expression
export interface DevToolFieldDetails { errors: import('./types.js').ValidationIssue[] } // 422 invalid: manual connection (paths relative to the connection)

/** DevToolSaved.content per kind: sql / cql / redis = the text; mqtt = JSON SavedMqttContent; http = JSON HttpRequestSpec. */
export type SavedMqttContent = Omit<MqttPublishRequest, 'confirm'>;

// ───────────────────────────────────────────────────────────── endpoint table

const P = '/_opm/api/sites/{siteSlug}/devtools';
const C = `${P}/connections/{connectionId}`;

export const DEVTOOLS_API = {
  devtoolsInfo: ep<void, DevToolsInfo>()({ method: 'GET', path: `${P}/info`, auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: 'Switch (agent.json tools.devTools), limits, kinds, head containers / ports / volumes / secret slugs for the pickers.' }),
  // connections
  listConnections: ep<void, ListResponse<DevToolConnection>>()({ method: 'GET', path: `${P}/connections`, auth: S, perm: null, body: 'none', res: 'json', errors: ['forbidden', 'unavailable'], summary: 'Presets (head SiteConfig.devTools) then manual connections, with the pooled state. Literal credentials masked.' }),
  createConnection: ep<DevToolConnectionInput, DevToolConnection>()({ method: 'POST', path: `${P}/connections`, auth: SA, perm: null, body: 'json', res: 'json', ok: 201, errors: ['forbidden', 'invalid', 'rate-limited'], activities: ['devtools.connection-created'], summary: 'Create a manual connection (stored on the site).' }),
  updateConnection: ep<DevToolConnectionInput, DevToolConnection>()({ method: 'PUT', path: C, auth: SA, perm: null, body: 'json', res: 'json', errors: ['forbidden', 'not-found', 'invalid', 'conflict'], activities: ['devtools.connection-updated'], summary: 'Update a manual connection (presets → conflict: duplicate to edit). Literal value MASK keeps the stored one. Closes the pooled client.' }),
  deleteConnection: ep<void, { deleted: true }>()({ method: 'DELETE', path: C, auth: SA, perm: null, body: 'none', res: 'json', errors: ['forbidden', 'not-found', 'conflict'], activities: ['devtools.connection-deleted'], summary: 'Delete a manual connection (presets → conflict).' }),
  duplicateConnection: ep<DuplicateConnectionRequest, DevToolConnection>()({ method: 'POST', path: `${C}/duplicate`, auth: SA, perm: null, body: 'json', res: 'json', ok: 201, errors: ['forbidden', 'not-found'], activities: ['devtools.connection-created'], summary: 'Copy a preset (or manual) connection into a new manual one (expressions kept, literals copied).' }),
  testConnection: ep<void, TestConnectionResponse>()({ method: 'POST', path: `${C}/test`, auth: SA, perm: null, body: 'none', res: 'json', errors: ['forbidden', 'not-found', 'rate-limited'], activities: ['devtools.connection-tested'], summary: 'Connect (fresh client), ping, server version.' }),
  testDraftConnection: ep<DevToolConnectionInput, TestConnectionResponse, { connectionId?: string }>()({ method: 'POST', path: `${P}/connections/test`, auth: SA, perm: null, body: 'json', res: 'json', errors: ['forbidden', 'invalid', 'rate-limited'], activities: ['devtools.connection-tested'], summary: 'Test an unsaved connection form (connectionId = the saved connection whose stored literal a MASK value stands for; MASK without it → 422).' }),
  disconnect: ep<void, { closed: boolean }>()({ method: 'POST', path: `${C}/disconnect`, auth: SA, perm: null, body: 'none', res: 'json', errors: ['forbidden', 'not-found'], summary: 'Close the pooled client (the next use reconnects).' }),
  revealCredential: ep<void, RevealResponse, { field?: 'password' | 'token' }>()({ method: 'POST', path: `${C}/reveal`, auth: SA, perm: null, body: 'none', res: 'json', errors: ['forbidden', 'not-found', 'invalid', 'rate-limited'], activities: ['devtools.credential-revealed'], summary: 'A stored literal password / token (RevealValue 60 s, no-store, 30/min per session). Expressions are not revealed here (reveal the secret).' }),
  // schema
  schema: ep<void, SqlSchemaResponse, { refresh?: boolean }>()({ method: 'GET', path: `${C}/schema`, auth: S, perm: null, body: 'none', res: 'json', errors: ['forbidden', 'not-found', 'invalid', 'unavailable'], summary: 'postgres / sqlite / cassandra: namespaces → tables → columns, functions, row estimates (cached 60 s; refresh=1 reloads).' }),
  tableDetail: ep<void, TableDetailResponse, { namespace: string; table: string }>()({ method: 'GET', path: `${C}/schema/table`, auth: S, perm: null, body: 'none', res: 'json', errors: ['forbidden', 'not-found', 'invalid', 'unavailable'], summary: 'Indexes, constraints, DDL of one table.' }),
  // execute
  executeSql: ep<ExecuteSqlRequest, ExecuteResponse>()({ method: 'POST', path: `${C}/sql`, auth: SA, perm: null, body: 'json', res: 'json', errors: ['forbidden', 'not-found', 'invalid', 'conflict', 'rate-limited', 'unavailable'], activities: ['devtools.query'], summary: 'postgres / sqlite / cassandra statements (multi, params, explain). Destructive → 422 confirm-required unless confirm = connection name. readOnly → read-only transaction / refusal. Statement errors are results (200, ok false).' }),
  executeRedis: ep<ExecuteRedisRequest, ExecuteRedisResponse>()({ method: 'POST', path: `${C}/redis`, auth: SA, perm: null, body: 'json', res: 'json', errors: ['forbidden', 'not-found', 'invalid', 'rate-limited', 'unavailable'], activities: ['devtools.query'], summary: 'One Redis command (blocked: MONITOR, SUBSCRIBE family, SHUTDOWN, DEBUG, MODULE, SLAVEOF/REPLICAOF, MIGRATE, CLIENT KILL; FLUSHALL / FLUSHDB / DEL with a pattern-like key / CONFIG SET → confirm).' }),
  executeHttp: ep<ExecuteHttpRequest, ExecuteHttpResponse>()({ method: 'POST', path: `${C}/http`, auth: SA, perm: null, body: 'json', res: 'json', errors: ['forbidden', 'not-found', 'invalid', 'rate-limited'], activities: ['devtools.query'], summary: 'One HTTP request from the site server (timing, redirects, size). readOnly connections allow GET / HEAD / OPTIONS only.' }),
  cancel: ep<CancelRequest, CancelResponse>()({ method: 'POST', path: `${C}/cancel`, auth: SA, perm: null, body: 'json', res: 'json', errors: ['forbidden', 'not-found'], activities: ['devtools.query-cancelled'], summary: 'Cancel a running execution (pg: pg_cancel_backend via the driver; others: context cancel).' }),
  resultPage: ep<void, ResultPageResponse, { offset: number; limit?: number }>()({ method: 'GET', path: `${P}/results/{resultId}`, auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found'], summary: 'Another page of a buffered result (same session, 15 min).' }),
  resultValue: ep<void, Response, { row: number; col: number }>()({ method: 'GET', path: `${P}/results/{resultId}/value`, auth: S, perm: null, body: 'none', res: 'raw', errors: ['not-found'], summary: 'The full value of one cell (attachment).' }),
  resultExport: ep<void, Response, { format: 'csv' | 'json' | 'ndjson' }>()({ method: 'GET', path: `${P}/results/{resultId}/export`, auth: S, perm: null, body: 'none', res: 'raw', errors: ['not-found', 'invalid'], summary: 'Every buffered row as CSV / JSON / NDJSON (attachment).' }),
  resultBody: ep<void, Response>()({ method: 'GET', path: `${P}/results/{resultId}/body`, auth: S, perm: null, body: 'none', res: 'raw', errors: ['not-found'], summary: 'The full HTTP response body (≤ maxResponseBytes, original content type).' }),
  // redis browser (reads; values masked never — this is the operator's data)
  redisScan: ep<void, RedisScanResponse, { pattern?: string; cursor?: string; count?: number; type?: string }>()({ method: 'GET', path: `${C}/redis/keys`, auth: S, perm: null, body: 'none', res: 'json', errors: ['forbidden', 'not-found', 'invalid', 'unavailable'], summary: 'SCAN MATCH pattern COUNT n [TYPE t] + TYPE / TTL / length per key (≤ 1000 per call).' }),
  redisKey: ep<void, RedisKeyValueResponse, { key: string; cursor?: string; count?: number }>()({ method: 'GET', path: `${C}/redis/key`, auth: S, perm: null, body: 'none', res: 'json', errors: ['forbidden', 'not-found', 'unavailable'], summary: 'The value of one key by its type (paged for collections).' }),
  redisInfo: ep<void, RedisInfoResponse>()({ method: 'GET', path: `${C}/redis/info`, auth: S, perm: null, body: 'none', res: 'json', errors: ['forbidden', 'not-found', 'unavailable'], summary: 'INFO (all sections parsed), keyspace per db, SLOWLOG GET 20.' }),
  // mqtt
  mqttPublish: ep<MqttPublishRequest, MqttPublishResponse>()({ method: 'POST', path: `${C}/mqtt/publish`, auth: SA, perm: null, body: 'json', res: 'json', errors: ['forbidden', 'not-found', 'invalid', 'rate-limited', 'unavailable'], activities: ['devtools.mqtt-published'], summary: 'Publish one message (qos, retain). readOnly → forbidden.' }),
  mqttSession: ep<MqttSessionRequest, MqttSessionResponse>()({ method: 'POST', path: `${C}/mqtt/sessions`, auth: SA, perm: null, body: 'json', res: 'json', ok: 201, errors: ['forbidden', 'not-found', 'invalid', 'conflict', 'rate-limited', 'unavailable'], activities: ['devtools.mqtt-subscribed'], summary: 'Open a live subscription session (own MQTT client) → single-use stream ticket. ≤ 4 sessions per GUI session.' }),
  mqttStream: ep<void, Response, { ticket: string }>()({ method: 'GET', path: `${C}/mqtt/sessions/{sessionId}/stream`, auth: S, perm: null, body: 'none', res: 'raw', errors: ['forbidden', 'not-found'], summary: 'text/event-stream of the session (MqttStreamEvent). Closing it ends the session (devtools.mqtt-unsubscribed with the count).' }),
  mqttSubscribe: ep<MqttSubscription, { subscriptions: MqttSubscription[] }>()({ method: 'POST', path: `${C}/mqtt/sessions/{sessionId}/subscribe`, auth: SA, perm: null, body: 'json', res: 'json', errors: ['forbidden', 'not-found', 'invalid'], activities: ['devtools.mqtt-subscribed'], summary: 'Add a topic filter to a running session.' }),
  mqttUnsubscribe: ep<{ filter: string }, { subscriptions: MqttSubscription[] }>()({ method: 'POST', path: `${C}/mqtt/sessions/{sessionId}/unsubscribe`, auth: SA, perm: null, body: 'json', res: 'json', errors: ['forbidden', 'not-found'], summary: 'Remove a topic filter.' }),
  mqttCloseSession: ep<void, { closed: true }>()({ method: 'POST', path: `${C}/mqtt/sessions/{sessionId}/close`, auth: SA, perm: null, body: 'none', res: 'json', errors: ['not-found'], activities: ['devtools.mqtt-unsubscribed'], summary: 'End a session.' }),
  mqttTopics: ep<void, ListResponse<MqttTopicSeen>>()({ method: 'GET', path: `${C}/mqtt/topics`, auth: S, perm: null, body: 'none', res: 'json', errors: ['forbidden', 'not-found'], summary: 'Topics seen by this connection’s sessions on this site (last 1000, since the agent started).' }),
  // saved, environments, history
  listSaved: ep<void, ListResponse<DevToolSaved>, { kind?: DevToolKind; connectionId?: string }>()({ method: 'GET', path: `${P}/saved`, auth: S, perm: null, body: 'none', res: 'json', errors: ['forbidden'], summary: 'Saved queries / requests of the site.' }),
  createSaved: ep<DevToolSavedInput, DevToolSaved>()({ method: 'POST', path: `${P}/saved`, auth: SA, perm: null, body: 'json', res: 'json', ok: 201, errors: ['forbidden', 'invalid'], summary: 'Save a query / request.' }),
  updateSaved: ep<DevToolSavedInput, DevToolSaved>()({ method: 'PUT', path: `${P}/saved/{savedId}`, auth: SA, perm: null, body: 'json', res: 'json', errors: ['forbidden', 'not-found', 'invalid'], summary: 'Update a saved item.' }),
  deleteSaved: ep<void, { deleted: true }>()({ method: 'DELETE', path: `${P}/saved/{savedId}`, auth: SA, perm: null, body: 'none', res: 'json', errors: ['forbidden', 'not-found'], summary: 'Delete a saved item.' }),
  listEnvironments: ep<void, ListResponse<DevToolEnvironment>>()({ method: 'GET', path: `${P}/environments`, auth: S, perm: null, body: 'none', res: 'json', errors: ['forbidden'], summary: 'HTTP environments (secret values masked).' }),
  createEnvironment: ep<DevToolEnvironmentInput, DevToolEnvironment>()({ method: 'POST', path: `${P}/environments`, auth: SA, perm: null, body: 'json', res: 'json', ok: 201, errors: ['forbidden', 'invalid'], summary: 'Create an environment.' }),
  updateEnvironment: ep<DevToolEnvironmentInput, DevToolEnvironment>()({ method: 'PUT', path: `${P}/environments/{environmentId}`, auth: SA, perm: null, body: 'json', res: 'json', errors: ['forbidden', 'not-found', 'invalid'], summary: 'Update an environment (a secret value MASK keeps the stored one).' }),
  deleteEnvironment: ep<void, { deleted: true }>()({ method: 'DELETE', path: `${P}/environments/{environmentId}`, auth: SA, perm: null, body: 'none', res: 'json', errors: ['forbidden', 'not-found'], summary: 'Delete an environment.' }),
  revealEnvironmentValue: ep<void, RevealResponse, { name: string }>()({ method: 'POST', path: `${P}/environments/{environmentId}/reveal`, auth: SA, perm: null, body: 'none', res: 'json', errors: ['forbidden', 'not-found', 'rate-limited'], activities: ['devtools.credential-revealed'], summary: 'A secret environment value (RevealValue 60 s).' }),
  // mine = the actor the agent last saw declared by THIS GUI session (reads carry no Actor header)
  history: ep<void, PageResponse<DevToolHistoryItem>, { connectionId?: string; kind?: DevToolKind; actor?: string; mine?: boolean; cursor?: string; limit?: number }>()({ method: 'GET', path: `${P}/history`, auth: S, perm: null, body: 'none', res: 'json', errors: ['forbidden'], summary: 'devtools.query activities of the site (newest first; mine = the declared actor of this session).' }),
} as const;

export type DevToolsClient = Client<typeof DEVTOOLS_API>;

export function createDevToolsClient(baseUrl: string, getActorToken: () => string | null, init?: { fetch?: typeof fetch }): DevToolsClient {
  return createClient(
    DEVTOOLS_API,
    baseUrl,
    ({ endpoint }) => {
      if (endpoint.auth !== 'session+actor') return null;
      const t = getActorToken();
      return t ? `Actor ${t}` : null;
    },
    { fetch: init?.fetch, credentials: 'same-origin' },
  );
}

// ───────────────────────────────────────────────────────────── defaults

export function defaultDevToolAuth(kind: DevToolKind): DevToolAuth {
  return {
    username: kind === 'postgres' ? 'postgres' : null,
    password: { kind: 'none' },
    database: kind === 'redis' ? '0' : null,
    clientId: null,
    httpScheme: 'none',
    token: { kind: 'none' },
    apiKeyHeader: null,
  };
}
export function defaultDevToolOptions(): DevToolOptions {
  return { tls: false, tlsInsecure: false, tlsServerName: null, sslMode: null, timeoutSec: null, maxRows: null, consistency: null, scheme: null, basePath: null, followRedirects: null, defaultHeaders: [], cleanSession: null, keepAliveSec: null };
}
export function defaultDevToolTarget(kind: DevToolKind): DevToolTarget {
  if (kind === 'sqlite') return { kind: 'volume-file', volumeSlug: '', path: '' };
  return { kind: 'container', containerSlug: '', portName: '' };
}
export function newDevToolPreset(kind: DevToolKind, taken: readonly string[] = []): DevToolPreset {
  let slug: string = kind;
  for (let i = 2; taken.includes(slug); i++) slug = `${kind}-${i}`;
  return { presetSlug: slug, name: DEVTOOL_KIND_LABEL[kind], description: '', kind, target: defaultDevToolTarget(kind), auth: defaultDevToolAuth(kind), options: defaultDevToolOptions(), readOnly: false };
}
/** Fill missing keys of a stored preset / connection (forward compatible readers). */
export function normalizeDevToolPreset(p: Partial<DevToolPreset> & { kind: DevToolKind }): DevToolPreset {
  const base = newDevToolPreset(p.kind);
  return {
    ...base,
    ...p,
    target: p.target ?? base.target,
    auth: { ...base.auth, ...(p.auth ?? {}) },
    options: { ...base.options, ...(p.options ?? {}), defaultHeaders: p.options?.defaultHeaders ?? [] },
  } as DevToolPreset;
}
/** SiteConfig.devTools of a site ([] when absent — the key is omitted when empty, §21 #97 a). */
export function siteDevTools(sc: { devTools?: DevToolPreset[] | null }): DevToolPreset[] {
  return sc.devTools ?? [];
}
/** Human text of a target ("postgres:pg", "10.0.0.5:5432", "volume data → app.db"). */
export function devToolTargetText(t: DevToolTarget): string {
  switch (t.kind) {
    case 'container':
      return `${t.containerSlug || '?'}:${t.portName || '?'}`;
    case 'host':
      return `${t.host}:${t.port}`;
    case 'url':
      return t.url;
    case 'volume-file':
      return `volume ${t.volumeSlug || '?'} → ${t.path || '?'}`;
    case 'container-file':
      return `${t.containerSlug || '?'}:${t.path || '?'}`;
  }
}
/** Secret slugs a preset references (`{{ secret.x }}` in any auth field / header). */
export function devToolSecretRefs(p: Pick<DevToolPreset, 'auth' | 'options' | 'target'>): string[] {
  const out = new Set<string>();
  const scan = (s: string | null | undefined) => {
    if (!s) return;
    for (const m of s.matchAll(/secret\.([a-z][a-z0-9-]*)/g)) out.add(m[1]!);
  };
  const cred = (c: DevToolCredential) => {
    if (c.kind === 'expr') scan(c.expr);
  };
  scan(p.auth.username);
  scan(p.auth.database);
  scan(p.auth.clientId);
  cred(p.auth.password);
  cred(p.auth.token);
  for (const h of p.options.defaultHeaders) scan(h.value);
  if (p.target.kind === 'url') scan(p.target.url);
  return [...out].sort();
}
export function devToolVarRefs(p: Pick<DevToolPreset, 'auth' | 'options' | 'target'>): string[] {
  const out = new Set<string>();
  const scan = (s: string | null | undefined) => {
    if (!s) return;
    for (const m of s.matchAll(/var\.([a-z][a-z0-9_-]*)/g)) out.add(m[1]!);
  };
  scan(p.auth.username);
  scan(p.auth.database);
  scan(p.auth.clientId);
  if (p.auth.password.kind === 'expr') scan(p.auth.password.expr);
  if (p.auth.token.kind === 'expr') scan(p.auth.token.expr);
  for (const h of p.options.defaultHeaders) scan(h.value);
  if (p.target.kind === 'url') scan(p.target.url);
  return [...out].sort();
}

/** §21 #99: the expression fields of a preset (username, database, clientId, apiKeyHeader, password / token, header values, url). */
export function devToolExprValues(p: Pick<DevToolPreset, 'auth' | 'options' | 'target'>): string[] {
  const out: string[] = [];
  const add = (s: string | null | undefined) => {
    if (s && s.includes('{{')) out.push(s);
  };
  add(p.auth.username);
  add(p.auth.database);
  add(p.auth.clientId);
  add(p.auth.apiKeyHeader);
  if (p.auth.password.kind === 'expr') add(p.auth.password.expr);
  if (p.auth.token.kind === 'expr') add(p.auth.token.expr);
  for (const h of p.options.defaultHeaders ?? []) add(h.value);
  if (p.target.kind === 'url') add(p.target.url);
  return out;
}
const CONTAINER_ENV_REF_RE = /containers\.([a-z0-9][a-z0-9-]*)\.env\.([A-Za-z0-9][A-Za-z0-9_-]*)/g;
/** §21 #99: `containers.<c>.env.<NAME>` references of a preset ("<c>.<NAME>", sorted unique). */
export function devToolContainerEnvRefs(p: Pick<DevToolPreset, 'auth' | 'options' | 'target'>): string[] {
  const out = new Set<string>();
  for (const v of devToolExprValues(p)) for (const m of v.matchAll(CONTAINER_ENV_REF_RE)) out.add(`${m[1]}.${m[2]}`);
  return [...out].sort();
}
/**
 * §21 #99: agent version from which a site resolves `containers.<c>.env.<NAME>` in presets (field devtool). An older
 * agent (0.11.0) refuses a revision whose presets use it (E_UNKNOWN_PATH at staging): the cloud warns per site.
 */
export const DEVTOOL_CONTAINER_ENV_MIN_AGENT = '0.11.1';
/**
 * §21 #103: agent version from which a preset may hold a static password / token (`{ kind: 'literal', value }`, the
 * "Value" mode). Older agents refuse such a revision (V_DEVTOOL_LITERAL was an error, and the preset resolver refused
 * literals): the cloud warns per site (never blocks).
 */
export const DEVTOOL_LITERAL_MIN_AGENT = '0.13.1';
/** §21 #103: the credential fields of a preset that hold a static value ("password" / "token"). */
export function devToolLiteralFields(p: Pick<DevToolPreset, 'auth'>): ('password' | 'token')[] {
  const out: ('password' | 'token')[] = [];
  if (p.auth?.password?.kind === 'literal') out.push('password');
  if (p.auth?.token?.kind === 'literal') out.push('token');
  return out;
}
/** §21 #103: the warning shown under a static value (cloud presets). */
export const DEVTOOL_LITERAL_WARNING = 'Stored in plain text in the config — anyone who can read the config sees it; use a secret for production.';

/** The preset fields a container env suggestion can fill. */
export type DevToolEnvField = 'username' | 'password' | 'database' | 'clientId' | 'token';
/**
 * §21 #99: well-known env names of service images, per preset kind and field (exact names first, in order of preference).
 * PostgreSQL: the official image (POSTGRES_USER / POSTGRES_PASSWORD / POSTGRES_DB), libpq (PGUSER …), Bitnami
 * (POSTGRESQL_*). Redis: Bitnami REDIS_PASSWORD, redis-cli REDISCLI_AUTH, ACL user. Cassandra: Bitnami CASSANDRA_*.
 * MQTT: Mosquitto / EMQX style user + password. HTTP: basic / token conventions.
 */
export const DEVTOOL_ENV_HINTS: Record<DevToolKind, Partial<Record<DevToolEnvField, readonly string[]>>> = {
  postgres: {
    username: ['POSTGRES_USER', 'PGUSER', 'POSTGRESQL_USERNAME', 'POSTGRES_USERNAME', 'DB_USER', 'DATABASE_USER'],
    password: ['POSTGRES_PASSWORD', 'PGPASSWORD', 'POSTGRESQL_PASSWORD', 'DB_PASSWORD', 'DATABASE_PASSWORD'],
    database: ['POSTGRES_DB', 'PGDATABASE', 'POSTGRESQL_DATABASE', 'DB_NAME', 'DATABASE_NAME'],
  },
  redis: {
    username: ['REDIS_USERNAME', 'REDIS_USER', 'REDIS_ACL_USERNAME'],
    password: ['REDIS_PASSWORD', 'REDISCLI_AUTH', 'REDIS_PASS', 'REDIS_AUTH', 'REQUIREPASS'],
  },
  cassandra: {
    username: ['CASSANDRA_USER', 'CASSANDRA_USERNAME'],
    password: ['CASSANDRA_PASSWORD', 'CASSANDRA_PASS'],
    database: ['CASSANDRA_KEYSPACE', 'KEYSPACE'],
  },
  mqtt: {
    username: ['MQTT_USERNAME', 'MQTT_USER', 'MOSQUITTO_USERNAME', 'MOSQUITTO_USER', 'EMQX_DASHBOARD__DEFAULT_USERNAME'],
    password: ['MQTT_PASSWORD', 'MQTT_PASS', 'MOSQUITTO_PASSWORD', 'EMQX_DASHBOARD__DEFAULT_PASSWORD'],
    clientId: ['MQTT_CLIENT_ID', 'MQTT_CLIENTID'],
  },
  http: {
    username: ['BASIC_AUTH_USER', 'BASIC_AUTH_USERNAME', 'HTTP_USER', 'ADMIN_USER', 'ADMIN_USERNAME'],
    password: ['BASIC_AUTH_PASSWORD', 'HTTP_PASSWORD', 'ADMIN_PASSWORD'],
    token: ['API_TOKEN', 'API_KEY', 'AUTH_TOKEN', 'ACCESS_TOKEN', 'BEARER_TOKEN'],
  },
  sqlite: {},
};
/** Fallback patterns when no well-known name matches (any kind). */
const DEVTOOL_ENV_PATTERNS: Record<DevToolEnvField, RegExp> = {
  username: /(^|_)(USER|USERNAME|LOGIN)$/,
  password: /(^|_)(PASSWORD|PASS|PASSWD|PWD)$/,
  database: /(^|_)(DB|DATABASE|DBNAME|KEYSPACE)$/,
  clientId: /(^|_)CLIENT_?ID$/,
  token: /(^|_)(TOKEN|API_?KEY)$/,
};
/**
 * §21 #99: the env names of a container worth suggesting for a preset field: the well-known names of the kind in their
 * order of preference, then names matching the field pattern (alphabetical). At most `max` (3).
 */
export function suggestDevToolEnv(kind: DevToolKind, field: DevToolEnvField, envNames: readonly string[], max = 3): string[] {
  const known = DEVTOOL_ENV_HINTS[kind][field] ?? [];
  const out: string[] = known.filter((n) => envNames.includes(n));
  const re = DEVTOOL_ENV_PATTERNS[field];
  for (const n of [...envNames].sort()) if (!out.includes(n) && re.test(n.toUpperCase())) out.push(n);
  return out.slice(0, max);
}
/** `{{ containers.<c>.env.<NAME> }}` */
export const containerEnvExpr = (containerSlug: string, name: string) => `{{ containers.${containerSlug}.env.${name} }}`;
/** `{{ containers.<c>.env.<NAME> }}` (only that) → { containerSlug, name } */
export function containerEnvOfExpr(v: string | null | undefined): { containerSlug: string; name: string } | null {
  const m = /^\{\{\s*containers\.([a-z0-9][a-z0-9-]*)\.env\.([A-Za-z0-9][A-Za-z0-9_-]*)\s*\}\}$/.exec((v ?? '').trim());
  return m ? { containerSlug: m[1]!, name: m[2]! } : null;
}

// ───────────────────────────────────────────────────────────── statement classification (vectors: testdata/devtools-vectors.json; Go: internal/devtools/classify.go)

export interface StatementClass {
  /** changes data or schema (refused on readOnly connections) */
  write: boolean;
  /** needs the typed confirmation (connection name) */
  destructive: boolean;
  /** human reasons ("DROP TABLE", "DELETE without WHERE") */
  reasons: string[];
}

/**
 * Split SQL / CQL text into statements on ";" outside of '…', "…", `…`, $tag$…$tag$, -- and /* *\/ comments.
 * Empty statements (only whitespace / comments) are dropped. Positions are not kept.
 */
export function splitSqlStatements(text: string): string[] {
  const out: string[] = [];
  let cur = '';
  let i = 0;
  const n = text.length;
  while (i < n) {
    const ch = text[i]!;
    const nx = text[i + 1];
    if (ch === '-' && nx === '-') {
      const e = text.indexOf('\n', i);
      const end = e < 0 ? n : e;
      cur += text.slice(i, end);
      i = end;
      continue;
    }
    if (ch === '/' && nx === '*') {
      const e = text.indexOf('*/', i + 2);
      const end = e < 0 ? n : e + 2;
      cur += text.slice(i, end);
      i = end;
      continue;
    }
    if (ch === "'" || ch === '"' || ch === '`') {
      let j = i + 1;
      while (j < n) {
        if (text[j] === ch) {
          if (text[j + 1] === ch) {
            j += 2;
            continue;
          }
          break;
        }
        j++;
      }
      const end = Math.min(n, j + 1);
      cur += text.slice(i, end);
      i = end;
      continue;
    }
    if (ch === '$') {
      const m = /^\$([A-Za-z_][A-Za-z0-9_]*)?\$/.exec(text.slice(i));
      if (m && !(i > 0 && /[A-Za-z0-9_]/.test(text[i - 1]!))) {
        const tag = m[0];
        const e = text.indexOf(tag, i + tag.length);
        const end = e < 0 ? n : e + tag.length;
        cur += text.slice(i, end);
        i = end;
        continue;
      }
    }
    if (ch === ';') {
      if (stripSqlComments(cur).trim() !== '') out.push(cur.trim());
      cur = '';
      i++;
      continue;
    }
    cur += ch;
    i++;
  }
  if (stripSqlComments(cur).trim() !== '') out.push(cur.trim());
  return out;
}

/** Remove -- and /* *\/ comments and blank out string literals (for keyword scanning). */
export function stripSqlComments(s: string): string {
  let out = '';
  let i = 0;
  const n = s.length;
  while (i < n) {
    const ch = s[i]!;
    const nx = s[i + 1];
    if (ch === '-' && nx === '-') {
      const e = s.indexOf('\n', i);
      i = e < 0 ? n : e;
      out += ' ';
      continue;
    }
    if (ch === '/' && nx === '*') {
      const e = s.indexOf('*/', i + 2);
      i = e < 0 ? n : e + 2;
      out += ' ';
      continue;
    }
    if (ch === "'" || ch === '"' || ch === '`') {
      let j = i + 1;
      while (j < n) {
        if (s[j] === ch) {
          if (s[j + 1] === ch) {
            j += 2;
            continue;
          }
          break;
        }
        j++;
      }
      // keep quoted identifiers as a word, drop string contents
      out += ch === "'" ? " '' " : ' ident ';
      i = Math.min(n, j + 1);
      continue;
    }
    if (ch === '$') {
      const m = /^\$([A-Za-z_][A-Za-z0-9_]*)?\$/.exec(s.slice(i));
      if (m && !(i > 0 && /[A-Za-z0-9_]/.test(s[i - 1]!))) {
        const e = s.indexOf(m[0], i + m[0].length);
        i = e < 0 ? n : e + m[0].length;
        out += " '' ";
        continue;
      }
    }
    out += ch;
    i++;
  }
  return out;
}

const READ_SQL = new Set(['SELECT', 'SHOW', 'EXPLAIN', 'DESCRIBE', 'DESC', 'VALUES', 'TABLE', 'WITH', 'PRAGMA', 'USE', 'SET', 'BEGIN', 'START', 'COMMIT', 'END', 'ROLLBACK', 'LISTEN', 'FETCH', 'DECLARE', 'CLOSE', 'SAVEPOINT', 'RELEASE', 'LIST', 'CONSISTENCY', 'TRACING', 'PAGING']);

/**
 * Classify one SQL (postgres / sqlite) or CQL statement.
 * destructive: DROP …, TRUNCATE, ALTER … DROP / ALTER TABLE … RENAME, DELETE / UPDATE without WHERE, VACUUM FULL,
 *   pg: DROP OWNED / REASSIGN OWNED, REVOKE, ALTER SYSTEM; cql: DROP KEYSPACE|TABLE|…, TRUNCATE.
 * write: anything not in the read set (+ WITH … containing INSERT/UPDATE/DELETE, SELECT … INTO, SELECT … FOR UPDATE,
 *   PRAGMA x = y, sqlite ATTACH / DETACH / VACUUM, pg COPY … FROM, CALL, DO, SET ROLE … keeps read).
 */
export function classifyStatement(kind: 'postgres' | 'sqlite' | 'cassandra', statement: string): StatementClass {
  const s = stripSqlComments(statement).replace(/\s+/g, ' ').trim();
  const up = s.toUpperCase();
  const words = up.split(/[\s(;]+/).filter(Boolean);
  const first = words[0] ?? '';
  const reasons: string[] = [];
  let write = !READ_SQL.has(first);
  const has = (re: RegExp) => re.test(up);
  if (first === 'WITH' && has(/\b(INSERT|UPDATE|DELETE|MERGE)\b/)) write = true;
  if (first === 'SELECT' && (has(/\bINTO\b/) && !has(/\bINSERT\b/) ? kind === 'postgres' : false)) write = true;
  if (first === 'SELECT' && has(/\bFOR (UPDATE|NO KEY UPDATE|SHARE|KEY SHARE)\b/)) write = true;
  if (first === 'PRAGMA' && has(/=/)) write = true;
  if (first === 'SET' && kind === 'postgres' && has(/^SET (SESSION )?(TRANSACTION|CHARACTERISTICS)?.*READ WRITE\b/)) write = true;
  if (first === 'EXPLAIN' && has(/\bANALY[SZ]E\b/) && has(/\b(INSERT|UPDATE|DELETE|MERGE)\b/)) write = true;
  // destructive
  if (first === 'DROP') reasons.push('DROP ' + (words[1] === 'MATERIALIZED' ? 'MATERIALIZED VIEW' : words[1] ?? ''));
  if (first === 'TRUNCATE') reasons.push('TRUNCATE');
  if (first === 'ALTER' && has(/\bDROP\b/)) reasons.push('ALTER … DROP');
  if (first === 'ALTER' && has(/\bRENAME\b/)) reasons.push('ALTER … RENAME');
  if (first === 'ALTER' && words[1] === 'SYSTEM') reasons.push('ALTER SYSTEM');
  const dmlFirst = first === 'WITH' ? (/\b(DELETE|UPDATE)\b/.exec(up)?.[1] ?? '') : first;
  if (dmlFirst === 'DELETE' && !has(/\bWHERE\b/)) reasons.push('DELETE without WHERE');
  if (dmlFirst === 'UPDATE' && !has(/\bWHERE\b/)) reasons.push('UPDATE without WHERE');
  if (first === 'VACUUM' && has(/\bFULL\b/)) reasons.push('VACUUM FULL');
  if (first === 'REASSIGN' || (first === 'DROP' && words[1] === 'OWNED')) reasons.splice(0, reasons.length, first === 'REASSIGN' ? 'REASSIGN OWNED' : 'DROP OWNED');
  if (first === 'REVOKE') reasons.push('REVOKE');
  if (kind === 'postgres' && first === 'COPY' && has(/\bPROGRAM\b/)) reasons.push('COPY … PROGRAM (runs a command on the database server)');
  else if (kind === 'postgres' && first === 'COPY' && /\b(TO|FROM)\s+''/.test(up)) reasons.push('COPY to / from a server file');
  if (kind === 'postgres' && has(/\b(PG_READ_FILE|PG_READ_BINARY_FILE|LO_IMPORT|LO_EXPORT|PG_LS_DIR)\s*\(/)) reasons.push('server file access function');
  if (first === 'DELETE' || first === 'UPDATE' || first === 'INSERT' || first === 'MERGE' || first === 'UPSERT' || first === 'REPLACE') write = true;
  return { write: write || reasons.length > 0, destructive: reasons.length > 0, reasons: reasons.map((r) => r.trim()) };
}

/** Redis commands that are refused entirely (blocking / connection-hijacking / server-administration). */
export const REDIS_BLOCKED = ['MONITOR', 'SUBSCRIBE', 'PSUBSCRIBE', 'SSUBSCRIBE', 'UNSUBSCRIBE', 'PUNSUBSCRIBE', 'SUNSUBSCRIBE', 'SHUTDOWN', 'DEBUG', 'MODULE', 'SLAVEOF', 'REPLICAOF', 'MIGRATE', 'SYNC', 'PSYNC', 'FAILOVER', 'QUIT', 'RESET', 'HELLO', 'SELECT', 'WAIT', 'WAITAOF', 'CLIENT KILL', 'CLIENT PAUSE', 'CLIENT NO-EVICT', 'CLIENT REPLY', 'ACL SAVE', 'ACL LOAD', 'ACL SETUSER', 'ACL DELUSER', 'CONFIG REWRITE', 'CLUSTER RESET', 'BGREWRITEAOF', 'BGSAVE', 'SAVE', 'LASTSAVE'] as const;
/** Read-only Redis commands (everything else is a write on a readOnly connection). */
export const REDIS_READ = ['GET', 'MGET', 'GETRANGE', 'STRLEN', 'EXISTS', 'TYPE', 'TTL', 'PTTL', 'EXPIRETIME', 'PEXPIRETIME', 'KEYS', 'SCAN', 'HGET', 'HMGET', 'HGETALL', 'HKEYS', 'HVALS', 'HLEN', 'HEXISTS', 'HSCAN', 'HSTRLEN', 'HRANDFIELD', 'LRANGE', 'LLEN', 'LINDEX', 'LPOS', 'SMEMBERS', 'SISMEMBER', 'SMISMEMBER', 'SCARD', 'SSCAN', 'SRANDMEMBER', 'SINTER', 'SUNION', 'SDIFF', 'SINTERCARD', 'ZRANGE', 'ZRANGEBYSCORE', 'ZREVRANGE', 'ZREVRANGEBYSCORE', 'ZRANGEBYLEX', 'ZREVRANGEBYLEX', 'ZSCORE', 'ZMSCORE', 'ZCARD', 'ZCOUNT', 'ZLEXCOUNT', 'ZRANK', 'ZREVRANK', 'ZSCAN', 'ZRANDMEMBER', 'ZINTER', 'ZUNION', 'ZDIFF', 'ZINTERCARD', 'XRANGE', 'XREVRANGE', 'XLEN', 'XREAD', 'XINFO', 'XPENDING', 'GEOPOS', 'GEODIST', 'GEOHASH', 'GEOSEARCH', 'GEORADIUS_RO', 'GEORADIUSBYMEMBER_RO', 'PFCOUNT', 'BITCOUNT', 'BITPOS', 'GETBIT', 'BITFIELD_RO', 'DUMP', 'OBJECT', 'MEMORY', 'RANDOMKEY', 'DBSIZE', 'INFO', 'PING', 'ECHO', 'TIME', 'LOLWUT', 'COMMAND', 'CONFIG GET', 'CLIENT LIST', 'CLIENT INFO', 'CLIENT GETNAME', 'CLIENT ID', 'SLOWLOG', 'LATENCY', 'ROLE', 'LCS', 'SORT_RO', 'JSON.GET', 'JSON.MGET', 'JSON.TYPE', 'JSON.STRLEN', 'JSON.ARRLEN', 'JSON.OBJKEYS', 'JSON.OBJLEN', 'FT.SEARCH', 'FT.INFO', 'FT._LIST', 'TS.GET', 'TS.RANGE', 'TS.MRANGE', 'TS.INFO', 'PUBSUB', 'ACL WHOAMI', 'ACL LIST', 'ACL USERS', 'ACL CAT', 'FUNCTION LIST', 'SCRIPT EXISTS'] as const;

/** Command name used for classification: "CONFIG GET", "CLIENT KILL", "ACL SETUSER" for container commands, else argv[0]. */
export function redisCommandName(argv: readonly string[]): string {
  const a = (argv[0] ?? '').toUpperCase();
  if (['CONFIG', 'CLIENT', 'ACL', 'CLUSTER', 'FUNCTION', 'SCRIPT', 'XINFO', 'OBJECT', 'MEMORY', 'SLOWLOG', 'LATENCY', 'PUBSUB', 'COMMAND', 'XGROUP'].includes(a) && argv[1]) return `${a} ${argv[1].toUpperCase()}`;
  return a;
}
export interface RedisCommandClass extends StatementClass {
  blocked: boolean;
}
/**
 * blocked: REDIS_BLOCKED. write: not in REDIS_READ (container commands match by "X Y" or by "X").
 * destructive: FLUSHALL, FLUSHDB, CONFIG SET, SCRIPT FLUSH, FUNCTION FLUSH, KEYS * is NOT destructive (read; but slow),
 *   DEL / UNLINK with any key containing * ? [ (a pattern-like key — Redis deletes literally, operators usually meant a
 *   pattern) or with more than 100 keys, EVAL / EVALSHA / FCALL (arbitrary scripts).
 */
export function classifyRedis(argv: readonly string[]): RedisCommandClass {
  const name = redisCommandName(argv);
  const first = (argv[0] ?? '').toUpperCase();
  const blocked = (REDIS_BLOCKED as readonly string[]).includes(name) || (REDIS_BLOCKED as readonly string[]).includes(first);
  const read = (REDIS_READ as readonly string[]).includes(name) || (name === first && (REDIS_READ as readonly string[]).includes(first)) || ((REDIS_READ as readonly string[]).includes(first) && !['CONFIG', 'CLIENT', 'ACL', 'CLUSTER', 'FUNCTION', 'SCRIPT', 'XGROUP'].includes(first));
  const reasons: string[] = [];
  if (first === 'FLUSHALL' || first === 'FLUSHDB') reasons.push(first);
  if (name === 'CONFIG SET') reasons.push('CONFIG SET');
  if (name === 'SCRIPT FLUSH' || name === 'FUNCTION FLUSH') reasons.push(name);
  if ((first === 'DEL' || first === 'UNLINK') && argv.slice(1).some((k) => /[*?[]/.test(k))) reasons.push(`${first} with a pattern-like key`);
  if ((first === 'DEL' || first === 'UNLINK') && argv.length - 1 > 100) reasons.push(`${first} of more than 100 keys`);
  if (first === 'EVAL' || first === 'EVALSHA' || first === 'FCALL' || first === 'EVAL_RO') reasons.push(`${first} (script)`);
  return { blocked, write: !read || reasons.length > 0, destructive: reasons.length > 0, reasons };
}

/** Parse a redis-cli style line: words separated by spaces, "double" (with \n \t \" \\ \xHH escapes) and 'single' quotes. */
export function parseRedisCommandLine(line: string): string[] | null {
  const out: string[] = [];
  let i = 0;
  const n = line.length;
  while (i < n) {
    while (i < n && /\s/.test(line[i]!)) i++;
    if (i >= n) break;
    let w = '';
    let quoted = false;
    while (i < n && !/\s/.test(line[i]!)) {
      const ch = line[i]!;
      if (ch === '"') {
        quoted = true;
        i++;
        let closed = false;
        while (i < n) {
          const c = line[i]!;
          if (c === '\\' && i + 1 < n) {
            const e = line[i + 1]!;
            if (e === 'x' && /^[0-9a-fA-F]{2}$/.test(line.slice(i + 2, i + 4))) {
              w += String.fromCharCode(parseInt(line.slice(i + 2, i + 4), 16));
              i += 4;
              continue;
            }
            w += e === 'n' ? '\n' : e === 'r' ? '\r' : e === 't' ? '\t' : e === 'b' ? '\b' : e === 'a' ? '\x07' : e;
            i += 2;
            continue;
          }
          if (c === '"') {
            closed = true;
            i++;
            break;
          }
          w += c;
          i++;
        }
        if (!closed) return null;
        continue;
      }
      if (ch === "'") {
        quoted = true;
        i++;
        let closed = false;
        while (i < n) {
          const c = line[i]!;
          if (c === '\\' && line[i + 1] === "'") {
            w += "'";
            i += 2;
            continue;
          }
          if (c === "'") {
            closed = true;
            i++;
            break;
          }
          w += c;
          i++;
        }
        if (!closed) return null;
        continue;
      }
      w += ch;
      i++;
    }
    if (w !== '' || quoted) out.push(w);
  }
  return out;
}

/** HTTP methods allowed on a readOnly connection. */
export const HTTP_READ_METHODS = ['GET', 'HEAD', 'OPTIONS'] as const;
/** Header names whose values are masked in activities / history (case-insensitive), plus any name matching SECRET_NAME_RE. */
export const SENSITIVE_HEADERS = ['authorization', 'proxy-authorization', 'cookie', 'set-cookie', 'x-api-key', 'api-key', 'x-auth-token', 'x-access-token'];
/** Parameter / field / header names that look like credentials. */
export const SECRET_NAME_RE = /(pass(word|wd)?|secret|token|api[-_]?key|apikey|auth|credential|private|session|cookie|signature|sig|access[-_]?key|client[-_]?secret|dsn)/i;
export const REDACTED = '••••••';

/**
 * Redact a statement for activities / history (also applied by the Go agent; vectors in devtools-vectors.json):
 *   - sql / cql: PASSWORD '…' (CREATE / ALTER ROLE|USER … PASSWORD), IDENTIFIED BY '…', WITH PASSWORD = '…' → '••••••';
 *     dblink / connection strings "password=…" → password=••••••
 *   - redis: AUTH <…>, HELLO … AUTH u p, CONFIG SET requirepass|masterauth <v>, ACL SETUSER … ><pw> → ••••••
 *   - every known secret value (≥ 4 chars) of the site, the connection's resolved credentials and the queryId → •••••• (done by the agent, not here)
 *   - truncated to STATEMENT_LOG_MAX bytes (UTF-8) with "…" appended.
 */
export function redactStatement(kind: DevToolKind, text: string): { text: string; truncated: boolean } {
  let t = text;
  if (kind === 'postgres' || kind === 'sqlite' || kind === 'cassandra') {
    t = t.replace(/(\bPASSWORD\s*=?\s*)('(?:[^']|'')*'|"(?:[^"]|"")*"|\$[A-Za-z_]*\$[\s\S]*?\$[A-Za-z_]*\$)/gi, `$1'${REDACTED}'`);
    t = t.replace(/(\bIDENTIFIED\s+BY\s+)('(?:[^']|'')*'|"(?:[^"]|"")*")/gi, `$1'${REDACTED}'`);
    t = t.replace(/(\bpassword\s*=\s*)([^\s'";]+)/gi, `$1${REDACTED}`);
  } else if (kind === 'redis') {
    const argv = parseRedisCommandLine(t);
    if (argv && argv.length) {
      const up = argv.map((a) => a.toUpperCase());
      const red = (i: number) => {
        if (i < argv.length) argv[i] = REDACTED;
      };
      if (up[0] === 'AUTH') for (let i = 1; i < argv.length; i++) red(i);
      if (up[0] === 'HELLO') {
        const k = up.indexOf('AUTH');
        if (k > 0) red(k + 2);
      }
      if (up[0] === 'CONFIG' && up[1] === 'SET') for (let i = 2; i + 1 < argv.length; i += 2) if (/pass|auth|secret|key/i.test(argv[i]!)) red(i + 1);
      if (up[0] === 'ACL' && up[1] === 'SETUSER') for (let i = 3; i < argv.length; i++) if (/^[>#<!]/.test(argv[i]!)) argv[i] = argv[i]![0] + REDACTED;
      if (up[0] === 'MIGRATE') {
        const k = up.indexOf('AUTH');
        if (k > 0) red(k + 1);
        const k2 = up.indexOf('AUTH2');
        if (k2 > 0) red(k2 + 2);
      }
      t = argv.map((a) => (a === '' || /[\s"']/.test(a) ? JSON.stringify(a) : a)).join(' ');
    }
  }
  return truncateUtf8(t, STATEMENT_LOG_MAX);
}

/** Redact an HTTP request for activities: "METHOD url" + masked query params + masked headers (+ body size, never the body). */
export function redactHttpRequest(method: string, url: string, headers: readonly [string, string][], bodyBytes: number): { text: string; truncated: boolean } {
  let u = url;
  const q = u.indexOf('?');
  if (q >= 0) {
    const base = u.slice(0, q);
    const parts = u
      .slice(q + 1)
      .split('&')
      .map((kv) => {
        const e = kv.indexOf('=');
        const k = e < 0 ? kv : kv.slice(0, e);
        let name = k;
        try {
          name = decodeURIComponent(k.replace(/\+/g, ' '));
        } catch {
          /* keep raw */
        }
        return e >= 0 && SECRET_NAME_RE.test(name) ? `${k}=${REDACTED}` : kv;
      });
    u = `${base}?${parts.join('&')}`;
  }
  u = u.replace(/^(\w+:\/\/)([^/@\s]*):([^/@\s]*)@/, `$1$2:${REDACTED}@`);
  const lines = [`${method.toUpperCase()} ${u}`];
  for (const [k, v] of headers) {
    const sens = SENSITIVE_HEADERS.includes(k.toLowerCase()) || SECRET_NAME_RE.test(k);
    if (!sens) {
      lines.push(`${k}: ${v}`);
      continue;
    }
    const sp = v.indexOf(' ');
    lines.push(`${k}: ${sp > 0 && /^(basic|bearer|digest|token|apikey)$/i.test(v.slice(0, sp)) ? v.slice(0, sp) + ' ' + REDACTED : REDACTED}`);
  }
  if (bodyBytes > 0) lines.push(`(body ${bodyBytes} bytes)`);
  return truncateUtf8(lines.join('\n'), STATEMENT_LOG_MAX);
}

export function truncateUtf8(s: string, max: number): { text: string; truncated: boolean } {
  const enc = new TextEncoder();
  const b = enc.encode(s);
  if (b.length <= max) return { text: s, truncated: false };
  let cut = max - 3; // room for "…"
  while (cut > 0 && (b[cut]! & 0xc0) === 0x80) cut--;
  return { text: new TextDecoder().decode(b.slice(0, cut)) + '…', truncated: true };
}

/** Typed confirmation for destructive statements: the connection name, exactly (trimmed). */
export function devToolConfirmOk(connectionName: string, typed: string | null | undefined): boolean {
  return (typed ?? '').trim() === connectionName.trim() && connectionName.trim() !== '';
}

/** Placeholders in an HTTP request: `{{name}}` environment variables vs OPM expressions (`{{ secret.x }}`…). */
export const OPM_EXPR_ROOTS = ['var', 'secret', 'site', 'self', 'deployment', 'containers', 'container', 'file'] as const;
export function isOpmPlaceholder(inner: string): boolean {
  const t = inner.trim();
  return (OPM_EXPR_ROOTS as readonly string[]).some((r) => t === r || t.startsWith(r + '.') || t.startsWith(r + '[')) || /\|/.test(t);
}
/** Resolve `{{name}}` environment placeholders (unknown names stay as they are; OPM expressions are left for the agent). */
export function applyEnvironment(text: string, vars: Record<string, string>): string {
  return text.replace(/\{\{\s*([^{}]+?)\s*\}\}/g, (m, inner: string) => (isOpmPlaceholder(inner) ? m : Object.prototype.hasOwnProperty.call(vars, inner.trim()) ? vars[inner.trim()]! : m));
}
