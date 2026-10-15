# Local adapter (SPEC §21 #108: OPM on a VM without Firebase)

`localPorts({ dataDir, publicUrl, consoleUrl?, triggers?, strict?, indexes? })` → `{ store, blobs, auth, close() }`.
Node 22 built-ins only (`node:sqlite`, `node:crypto`), plus `hash-wasm` (argon2id).

## Data dir

```
<dataDir>/db/global.sqlite                every document NOT under deployments/{id}
<dataDir>/db/deployments/<id>.sqlite      deployments/{id} itself (table "deployment") + everything under it
<dataDir>/blobs/<object path>             objects, same paths as the GCS bucket (blobs/sha256/…, files/…, staging/…)
<dataDir>/blobmeta/<object path>.json     sidecar {contentType, cacheControl, meta, createdMs, updatedMs}
<dataDir>/tmp/                            temp files of in-flight writes (renamed into place when complete)
<dataDir>/secrets/blob-hmac.key           32 random bytes, 0600 (or env OPM_BLOB_SECRET)
<dataDir>/secrets/auth-jwt.key            32 random bytes, 0600 (or env OPM_AUTH_SECRET)
```

Deployment ids must match `[A-Za-z0-9]{1,64}` because they are used as file names. Each file uses `journal_mode=WAL`,
`busy_timeout=5000` and `synchronous=NORMAL`. Open handles are cached (LRU, 256 entries). Writes to different
deployments take different locks and run in parallel. Several processes can safely use the same files at once
(opm-cloud, scripts, tests).

## Tables

The table name is the collection path with the document ids removed, joined with `__`. Examples: `services__images`,
`githubMirror__queue`, `templates__commits`. In a deployment file the table name is taken below `deployments/{id}`:
`commits`, `agentBranches__commits`, `volumeContent__versions`, and `deployment` for the deployment document itself.
Each table has this shape:

```sql
CREATE TABLE t (id TEXT PRIMARY KEY /* full document path */, parent TEXT NOT NULL /* collection path */,
                value TEXT NOT NULL /* JSON */, ix_<field> … )
```

**JSON encoding.** Values are stored as plain JSON, with these special cases:
- Timestamp is stored as `{"$timestamp":{"seconds":S,"nanos":N}}`.
- Buffer is stored as `{"$bytes":"<base64>"}`.
- Date becomes a Timestamp, as in Firestore.
- `undefined` properties are dropped, at any depth.

**Index columns.** `registry.ts` lists the `ix_<field>` columns of each table. A dotted field such as `a.b` becomes
`ix_a_b`. Every write fills the columns from the JSON, using this encoding:

| value | stored in the column as |
|---|---|
| string, number | as is |
| boolean | 0 / 1 |
| Timestamp | epoch ms |
| anything else | NULL |

`array-contains` filters run `json_each` over the stored JSON. Each table gets SQLite indexes on `(parent, ix)`, on
the composite shapes from firestore.indexes.json, and on `(ix)` for fields that collection-group queries use.

**Registry upgrades.** Tables are created lazily. Each file has a `_schema` table that records a hash of the registry
entry for every table. When that hash changes, the adapter runs `ALTER TABLE ADD COLUMN`, backfills the new column in
SQL from the JSON, and creates any missing indexes.

**Unregistered fields.** A query on a field with no index column falls back to an equivalent SQL expression over the
JSON. With `OPM_LOCAL_STRICT=1` (or `strict: true`) it throws instead, which is how the tests keep the registry
complete.

## Queries

- Comparisons are typed. A string filter never matches numbers, booleans or timestamps, and the other way round.
- Ordering:
  - Without any orderBy, a range filter orders results by its field.
  - After the last orderBy, results are ordered by document path, in that orderBy's direction.
  - Documents whose orderBy field is null or **missing are included**, sorted first in ascending order. Firestore
    leaves out documents that are missing the field; OPM never relies on that.
- Cursors: `startAfter(snapshot | values…)` is expanded into an OR form, so mixed sort directions work.
- Projection: `select()` projects in JavaScript.
- Multi-file queries:
  - `collectionGroup(id)` reads every table whose last collection id is `id`, in the global file and in every
    deployment file.
  - A query on the root `collection('deployments')` reads the `deployment` table of every deployment file.
  - Both merge-sort the results in JavaScript, with the same ordering SQLite uses (NULL < numbers < strings in UTF-8
    byte order), and then apply the limit.

## Transactions

Transactions are **optimistic, like Firestore**.

**Reads.** `tx.get` and `tx.getAll` read committed data and record what they read:
- for a document, its raw JSON (or "missing");
- for a query, the list of ids and values it returned.

Reading after a write is an error.

**Commit.** The commit runs synchronously, with no `await` in between:
1. `BEGIN IMMEDIATE` on every involved file (both read and written files), in canonical order: deployment files
   sorted by id, the **global file last**.
2. Re-validate every read under those write locks.
3. Apply the writes and the outbox rows.
4. `COMMIT` the deployment files first and **the global file last**.

If validation fails, the adapter rolls back everything and re-runs the callback with jittered backoff (default 5
attempts, then `StoreError aborted`). A query over many files (collection group, root deployments) also locks the
global file. That gives two such transactions a common lock, so a check like "is this slug free across all
deployments" is serializable.

**Batches and BulkWriter.** Batches take the same commit path without reads. The BulkWriter commits each write on its
own and honours `onWriteError`.

**Cross-file rule.** A crash between the two COMMITs keeps only the deployment-file part. Set `OPM_LOCAL_TRACE_XTX=1`
to log each transaction that spans more than one file (`local cross-db tx`, with `{files, paths}`).

## Outbox (Events port)

Every write whose path matches a trigger pattern inserts a row into `_outbox(seq, trigger, path, before, after,
createdMs, leaseUntil)`. The row goes into the same file, in the same SQLite transaction as the write. Patterns come
from `TRIGGER_SPECS` (core/trigger-specs.ts). A row is written when:
- `created`: the document did not exist before the write and exists after it;
- `written`: the document changed in any way, including a delete. A write that leaves the document unchanged adds no
  row.

**Dispatcher.** `startOutboxDispatcher(store, triggers, { concurrency, pollMs })`:
- wakes right after local commits, and polls the files whose mtime changed every `pollMs` (other processes write
  too);
- claims rows with a 15-minute lease and runs the handler;
- deletes the row once the handler settles. Errors are logged and dropped, like Cloud Functions with `retry: false`.
  Expired leases run again, so delivery is at-least-once.

`idle()` resolves when nothing is pending or running. `sweepDocTtl(store, now)` implements the Firestore TTL policies
(`agentNonces.expiresAt`, `rateLimits.expireAt`).

## Blobs

- Writes are atomic: a temp file is renamed into place, and a write stream's object appears only when it finishes.
- `list(prefix)` matches a raw string prefix, and `''` lists everything.
- `canCompose` is false.
- `sweep(rules)` applies the lifecycle rules (storage.lifecycle.json) and removes stale temp files.

**Read URLs.** A read URL looks like `${publicUrl}/_blob/<path, each segment URI-encoded>?exp=<ms>&sig=<b64url>[&rd=<disposition>]`.
`sig` is HMAC-SHA256 over `path\nexp\ndisposition`. `handleBlobHttp` serves `GET` and `HEAD` for these URLs:
- it answers 403 when the signature is bad or the URL has expired;
- it supports `Range` (206 / 416);
- it applies CORS for `allowedOrigins`, as in storage.cors.json.

## Auth

Users are stored in the global DB:

| document | holds |
|---|---|
| `authUsers/{uid}` | the user; the uid is 28 alphanumeric characters, or the uid passed to createUser |
| `authEmails/{sha256(email)}` | uniqueness claim for the e-mail, made in the same transaction as the user |
| `authRefresh/{sha256}` | refresh tokens: opaque, rotated on every use; replaying a rotated token revokes its whole family; valid 30 days |
| `authReset/{sha256}` | password-reset tokens: one-time, valid 1 hour |
| `authOidcState/{sha256(state)}`, `authOidcCode/{sha256}` | OIDC state and one-time codes |

- Passwords are hashed with argon2id (m=19456, t=2, p=1).
- ID tokens are HS256 JWTs: `{iss:'opm-local', aud:'opm', sub, uid, email, iat, exp: iat+3600}`.
- Revocation (password change, reset, `revokeRefreshTokens`) sets `validSinceSec` (second precision, like Firebase)
  and deletes the user's refresh tokens.

`handleAuthHttp` serves `/auth/config`, `/auth/login`, `/auth/refresh`, `/auth/logout` and `/auth/reset/*`, plus
`/auth/oidc/{start,callback,exchange}` when OIDC is configured. OIDC uses discovery, PKCE S256, state and nonce. The
id_token must be RS256 and is checked against the provider's JWKS; the e-mail must be verified, and `hd` must match
when `allowedDomain` is set. Login failures are limited in memory to 10 per 15 minutes for each IP + e-mail pair.

## Backups

`backupSqlite(dataDir, outDir)` copies every `.sqlite` file with `VACUUM INTO`, which gives a consistent copy while
the server is running. Back up the `blobs/`, `blobmeta/` and `secrets/` directories alongside it.
