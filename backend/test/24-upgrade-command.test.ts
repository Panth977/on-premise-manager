/**
 * SPEC-final §21 #92 — nothing may be one-time: the enrolled site's "Upgrade / reinstall command" is always viewable.
 * - enroll keeps the install token's plaintext → revealUpgradeCommand (activity install-command.revealed + audit, no-store)
 * - not stored (older tokens) → 410 not-stored → regenerateUpgradeCommand mints a SITE-BOUND maintenance token
 *   (upgrade-only script / binary / deps, never enrolls, no expiry, regenerate revokes the previous command)
 * - revokeAgent ends the enrollment's upgrade command; fresh-site install tokens keep their 24 h single-enroll rules
 * - heartbeat: lastSeenAt = server receive time; agentTime → clockSkew (agent − server); serverTime on Setup
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  createCloudClient,
  clockSkewWarning,
  type Activity,
  type AccessRule,
} from "@opm/shared";
import { verifySchema } from "../scripts/verify-schema.ts";
import {
  adb,
  BASE,
  enrollSite,
  helloDeployment,
  newSiteKey,
  person,
  raw,
  rejects,
  tinyScript,
} from "./helpers.ts";

async function cloudActivities(deploymentId: string): Promise<Activity[]> {
  const q = await adb
    .collection("deployments")
    .doc(deploymentId)
    .collection("activities")
    .where("siteSlug", "==", "_cloud")
    .get();
  return q.docs
    .map(
      (d) =>
        ({
          ...d.data(),
          data: JSON.parse(d.data()._data),
        }) as unknown as Activity,
    )
    .sort((a, b) => a.seq - b.seq);
}
async function auditOf(target: string) {
  return (
    await adb.collection("auditLog").where("target", "==", target).get()
  ).docs.map((d) => {
    const x = d.data();
    return { ...x, data: JSON.parse(x._data) } as Record<string, any>;
  });
}
const enrollBody = () => ({
  publicKey: newSiteKey().publicKey,
  agentVersion: "0.1.0-test",
  hostname: "h",
  ip: "127.0.0.1",
  advertiseHost: "127.0.0.1",
  agentPort: 7080,
  os: "darwin",
  arch: "arm64",
});

test("§21 #92 upgrade / reinstall command: install token kept, reveal logged, regenerate = maintenance token, rules", async (t) => {
  const admin = await person("admin");
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const toks = adb
    .collection("deployments")
    .doc(id)
    .collection("installTokens");
  const app = await enrollSite(admin, id, "app");
  const firstHash = (await toks.where("siteSlug", "==", "app").get()).docs[0]!
    .id;
  let m1 = {
    token: "",
    tokenHash: "",
    kind: "",
    value: "",
    command: "",
    enrolledAt: "",
  };

  await t.test(
    "enrolled site: the install token stays stored and is the site's upgrade command (view logged + audited)",
    async () => {
      const doc = (await toks.doc(firstHash).get()).data()!;
      assert.equal(doc.token, app.token, "enroll keeps the plaintext");
      assert.ok(doc.usedAt);
      const setup = await admin.api.getSetup({ deploymentId: id });
      assert.ok(
        Math.abs(Date.parse(setup.serverTime) - Date.now()) < 10_000,
        "serverTime",
      );
      const site = setup.sites.find((s) => s.siteSlug === "app")!;
      assert.deepEqual(
        { ...site.upgradeCommand, createdAt: null, createdBy: null },
        {
          tokenHash: firstHash,
          kind: "install",
          stored: true,
          createdAt: null,
          createdBy: null,
        },
      );
      const tv = site.tokens.find((x) => x.tokenHash === firstHash)!;
      assert.equal(tv.state, "used");
      assert.equal(tv.kind, "install");
      assert.equal(tv.upgradeCommand, true);
      assert.equal(
        tv.stored,
        false,
        "the fresh-site View stays off for a used token",
      );
      assert.ok(
        !JSON.stringify(setup).includes(app.token),
        "setup never carries the token",
      );
      // not enrolled → 404 (fresh sites use the install command)
      assert.equal(
        setup.sites.find((s) => s.siteSlug === "db")!.upgradeCommand,
        null,
      );
      await rejects(
        admin.api.revealUpgradeCommand({ deploymentId: id, siteSlug: "db" }),
        "not-found",
        404,
      );
      const r = await raw(
        "POST",
        `/v1/deployments/${id}/agents/app/upgrade-command/reveal`,
        { headers: { Authorization: admin.bearer } },
      );
      assert.equal(r.status, 200);
      assert.equal(r.headers.get("cache-control"), "no-store");
      const j = r.json as Record<string, unknown>;
      assert.equal(j.token, app.token);
      assert.equal(j.kind, "install");
      assert.equal(j.hideAfterSec, 60);
      assert.equal(j.value, j.command);
      assert.match(
        String(j.command),
        new RegExp(
          `^curl -fsSL -H "Authorization: Bearer ${app.token}" .+/v1/install/script \\| sudo sh$`,
        ),
      );
      const act = (await cloudActivities(id)).find(
        (a) => a.type === "install-command.revealed",
      )!;
      assert.deepEqual(act.data, {
        siteSlug: "app",
        tokenHash: firstHash,
        kind: "install",
      });
      assert.equal(act.actor, admin.uid);
      const au = (await auditOf(`deployments/${id}`)).filter(
        (e) =>
          e.action === "deployment.upgrade-command-revealed" &&
          e.outcome === "ok",
      );
      assert.equal(au.length, 1);
      assert.deepEqual(au[0]!.data, {
        siteSlug: "app",
        tokenHash: firstHash,
        kind: "install",
      });
      // the fresh-site reveal still says used
      assert.deepEqual(
        (
          await rejects(
            admin.api.revealInstallCommand({
              deploymentId: id,
              tokenHash: firstHash,
            }),
            "gone",
            410,
          )
        ).details,
        { reason: "used" },
      );
      // a new install command for an enrolled site stays a conflict
      await rejects(
        admin.api.createInstallToken({
          deploymentId: id,
          body: { siteSlug: "app" },
        }),
        "conflict",
        409,
      );
    },
  );

  await t.test(
    "older token (plaintext not stored) → 410 not-stored; Regenerate mints a maintenance token and revokes the old command",
    async () => {
      await toks.doc(firstHash).update({ token: null }); // like the user's Dev Server (migrate-g nulled it)
      let site = (await admin.api.getSetup({ deploymentId: id })).sites.find(
        (s) => s.siteSlug === "app",
      )!;
      assert.equal(site.upgradeCommand!.stored, false);
      assert.equal(site.upgradeCommand!.tokenHash, firstHash);
      assert.deepEqual(
        (
          await rejects(
            admin.api.revealUpgradeCommand({
              deploymentId: id,
              siteSlug: "app",
            }),
            "gone",
            410,
          )
        ).details,
        { reason: "not-stored" },
      );
      // the old command still works on the server until Regenerate (upgrade rule of §21 #86)
      assert.ok(
        (
          await raw("GET", "/v1/install/script", {
            headers: { Authorization: `Bearer ${app.token}` },
          })
        ).body
          .toString()
          .includes('UPGRADE_ONLY="1"'),
      );
      const r = await raw(
        "POST",
        `/v1/deployments/${id}/agents/app/upgrade-command`,
        { headers: { Authorization: admin.bearer } },
      );
      assert.equal(r.status, 201);
      assert.equal(r.headers.get("cache-control"), "no-store");
      m1 = r.json as typeof m1;
      assert.equal(m1.kind, "maintenance");
      assert.equal(m1.value, m1.command);
      const enrolledAt = (
        await adb
          .collection("deployments")
          .doc(id)
          .collection("agents")
          .doc("app")
          .get()
      ).data()!.enrolledAt;
      assert.equal(m1.enrolledAt, enrolledAt);
      const md = (await toks.doc(m1.tokenHash).get()).data()!;
      assert.deepEqual(
        {
          kind: md.kind,
          token: md.token,
          expiresAt: md.expiresAt,
          boundEnrolledAt: md.boundEnrolledAt,
          usedAt: md.usedAt,
          revokedAt: md.revokedAt,
        },
        {
          kind: "maintenance",
          token: m1.token,
          expiresAt: null,
          boundEnrolledAt: enrolledAt,
          usedAt: null,
          revokedAt: null,
        },
      );
      const old = (await toks.doc(firstHash).get()).data()!;
      assert.ok(
        old.revokedAt,
        "the enrollment's install token is revoked by Regenerate",
      );
      assert.equal(old.token, null);
      tinyScript(
        await raw("GET", "/v1/install/script", {
          headers: { Authorization: `Bearer ${app.token}` },
        }),
        /token revoked \(site app\)/,
      );
      const act = (await cloudActivities(id)).filter(
        (a) => a.type === "install-command.regenerated",
      );
      assert.equal(act.length, 1);
      assert.deepEqual(act[0]!.data, {
        siteSlug: "app",
        tokenHash: m1.tokenHash,
        revoked: [firstHash],
      });
      const au = (await auditOf(`deployments/${id}`)).filter(
        (e) =>
          e.action === "deployment.upgrade-command-regenerated" &&
          e.outcome === "ok",
      );
      assert.equal(au.length, 1);
      assert.deepEqual(au[0]!.data, {
        siteSlug: "app",
        tokenHash: m1.tokenHash,
        kind: "maintenance",
        revoked: [firstHash],
      });
      site = (await admin.api.getSetup({ deploymentId: id })).sites.find(
        (s) => s.siteSlug === "app",
      )!;
      assert.equal(site.upgradeCommand!.kind, "maintenance");
      assert.equal(site.upgradeCommand!.stored, true);
      assert.equal(site.upgradeCommand!.tokenHash, m1.tokenHash);
      const mv = site.tokens.find((x) => x.tokenHash === m1.tokenHash)!;
      assert.deepEqual(
        {
          kind: mv.kind,
          state: mv.state,
          expiresAt: mv.expiresAt,
          stored: mv.stored,
          upgradeCommand: mv.upgradeCommand,
        },
        {
          kind: "maintenance",
          state: "active",
          expiresAt: null,
          stored: false,
          upgradeCommand: true,
        },
      );
      const rv = await admin.api.revealUpgradeCommand({
        deploymentId: id,
        siteSlug: "app",
      });
      assert.equal(rv.token, m1.token);
      assert.equal(rv.kind, "maintenance");
      // the maintenance token is not a fresh-site install command
      assert.deepEqual(
        (
          await rejects(
            admin.api.revealInstallCommand({
              deploymentId: id,
              tokenHash: m1.tokenHash,
            }),
            "gone",
            410,
          )
        ).details,
        { reason: "maintenance" },
      );
    },
  );

  await t.test(
    "maintenance token: upgrade-only script + binary/deps/release accepted, never enrolls, never expires",
    async () => {
      const H = { Authorization: `Bearer ${m1.token}` };
      const s = await raw("GET", "/v1/install/script", { headers: H });
      assert.equal(s.status, 200);
      const sh = s.body.toString();
      assert.ok(sh.includes('UPGRADE_ONLY="1"'), "upgrade-only flow");
      assert.ok(
        sh.includes(
          "This upgrade command belongs to the enrolled site $SITE; it can only upgrade that server",
        ),
      );
      assert.ok(!sh.includes("This install command already enrolled"));
      assert.ok(sh.includes(`TOKEN="${m1.token}"`));
      // binary / release: the token is accepted (404 / 422 = past the auth check; no agent uploaded in this test)
      for (const p of [
        "/v1/install/binary?os=linux&arch=amd64&version=0.0.0-none",
        "/v1/install/release?os=linux&arch=amd64&version=0.0.0-none",
      ]) {
        const b = await raw("GET", p, { headers: H });
        assert.ok(
          b.status !== 401 && b.status !== 403 && b.status !== 410,
          `${p} → ${b.status}`,
        );
      }
      const anon = createCloudClient(BASE, () => `Bearer ${m1.token}`);
      const e = await rejects(
        anon.enroll({ body: enrollBody() }),
        "forbidden",
        403,
      );
      assert.equal((e.details as { reason?: string }).reason, "maintenance");
      // no 24 h expiry: the daily job keeps it, an old createdAt changes nothing
      await toks
        .doc(m1.tokenHash)
        .update({
          createdAt: new Date(Date.now() - 30 * 86400_000).toISOString(),
        });
      const { runDaily } = await import("../functions/src/jobs/daily.ts");
      await runDaily();
      const after = (await toks.doc(m1.tokenHash).get()).data()!;
      assert.equal(after.token, m1.token);
      assert.equal(after.revokedAt, null);
      assert.equal(
        (await raw("GET", "/v1/install/script", { headers: H })).status,
        200,
      );
      assert.ok(
        (await raw("GET", "/v1/install/script", { headers: H })).body
          .toString()
          .includes('UPGRADE_ONLY="1"'),
      );
    },
  );

  await t.test(
    "Regenerate again revokes the previous maintenance token",
    async () => {
      const m2 = await admin.api.regenerateUpgradeCommand({
        deploymentId: id,
        siteSlug: "app",
      });
      assert.notEqual(m2.token, m1.token);
      const prev = (await toks.doc(m1.tokenHash).get()).data()!;
      assert.ok(prev.revokedAt);
      assert.equal(prev.token, null);
      tinyScript(
        await raw("GET", "/v1/install/script", {
          headers: { Authorization: `Bearer ${m1.token}` },
        }),
        /token revoked \(site app\)/,
      );
      assert.equal(
        (
          await raw("GET", "/v1/install/binary?os=linux&arch=amd64", {
            headers: { Authorization: `Bearer ${m1.token}` },
          })
        ).status,
        410,
      );
      assert.ok(
        (
          await raw("GET", "/v1/install/script", {
            headers: { Authorization: `Bearer ${m2.token}` },
          })
        ).body
          .toString()
          .includes('UPGRADE_ONLY="1"'),
      );
      const site = (await admin.api.getSetup({ deploymentId: id })).sites.find(
        (s) => s.siteSlug === "app",
      )!;
      assert.equal(site.upgradeCommand!.tokenHash, m2.tokenHash);
      assert.equal(
        site.tokens.find((x) => x.tokenHash === m1.tokenHash)!.state,
        "revoked",
      );

      // a maintenance token bound to another (ended) enrollment never works
      const foreign = "opi_" + "x".repeat(43);
      const { createHash } = await import("node:crypto");
      const fh = createHash("sha256").update(foreign).digest("hex");
      await toks
        .doc(fh)
        .set({
          tokenHash: fh,
          siteSlug: "app",
          kind: "maintenance",
          token: null,
          expiresAt: null,
          boundEnrolledAt: "2020-01-01T00:00:00.000Z",
          usedAt: null,
          revokedAt: null,
          createdAt: new Date().toISOString(),
          createdBy: admin.uid,
        });
      tinyScript(
        await raw("GET", "/v1/install/script", {
          headers: { Authorization: `Bearer ${foreign}` },
        }),
        /belongs to an enrollment \(site app\) that was revoked/,
      );
      await toks.doc(fh).delete();

      // revokeAgent ends the enrollment's upgrade command
      await admin.api.revokeAgent({
        deploymentId: id,
        siteSlug: "app",
        body: { confirm: "app" },
      });
      const m2d = (await toks.doc(m2.tokenHash).get()).data()!;
      assert.ok(m2d.revokedAt);
      assert.equal(m2d.token, null);
      tinyScript(
        await raw("GET", "/v1/install/script", {
          headers: { Authorization: `Bearer ${m2.token}` },
        }),
        /token revoked \(site app\)/,
      );
      await rejects(
        admin.api.revealUpgradeCommand({ deploymentId: id, siteSlug: "app" }),
        "not-found",
        404,
      );
      await rejects(
        admin.api.regenerateUpgradeCommand({
          deploymentId: id,
          siteSlug: "app",
        }),
        "not-found",
        404,
      );
      // the site enrolls again with a fresh 24 h install token (single enroll; the new token becomes the upgrade command)
      const fresh = await admin.api.createInstallToken({
        deploymentId: id,
        body: { siteSlug: "app" },
      });
      assert.ok(
        Date.parse(fresh.expiresAt) - Date.now() > 23 * 3600_000 &&
          Date.parse(fresh.expiresAt) - Date.now() <= 24 * 3600_000 + 5000,
      );
      const anon = createCloudClient(BASE, () => `Bearer ${fresh.token}`);
      await anon.enroll({ body: enrollBody() });
      await rejects(anon.enroll({ body: enrollBody() }), "gone", 410); // never a second enroll
      const s2 = (await admin.api.getSetup({ deploymentId: id })).sites.find(
        (s) => s.siteSlug === "app",
      )!;
      assert.deepEqual(
        [
          s2.upgradeCommand!.tokenHash,
          s2.upgradeCommand!.kind,
          s2.upgradeCommand!.stored,
        ],
        [fresh.tokenHash, "install", true],
      );
      assert.equal(
        (
          await admin.api.revealUpgradeCommand({
            deploymentId: id,
            siteSlug: "app",
          })
        ).token,
        fresh.token,
      );
    },
  );

  await t.test(
    "permissions: setup.read cannot view or regenerate; tokens never leak into logs; schema clean",
    async () => {
      const all = { kind: "all" as const };
      const rules: AccessRule[] = [
        { resource: "deployments", actions: ["read"], scope: all },
        { resource: "deployment.setup", actions: ["read"], scope: all },
      ];
      const viewer = await person(rules);
      await rejects(
        viewer.api.revealUpgradeCommand({ deploymentId: id, siteSlug: "app" }),
        "forbidden",
        403,
      );
      await rejects(
        viewer.api.regenerateUpgradeCommand({
          deploymentId: id,
          siteSlug: "app",
        }),
        "forbidden",
        403,
      );
      const plain = (await toks.get()).docs
        .map((d) => d.data().token)
        .filter((x): x is string => typeof x === "string");
      const logs =
        (await adb.collection("auditLog").get()).docs
          .map((d) => JSON.stringify(d.data()))
          .join("\n") +
        (
          await adb
            .collection("deployments")
            .doc(id)
            .collection("activities")
            .get()
        ).docs
          .map((d) => JSON.stringify(d.data()))
          .join("\n");
      for (const p of [...plain, app.token])
        assert.ok(!logs.includes(p), "a token leaked into a log");
      assert.deepEqual(
        (await verifySchema(adb)).filter((l) => l.includes(id)),
        [],
      );
    },
  );
});

test("§21 #92 heartbeat: lastSeenAt = server receive time; agentTime → clockSkew (agent − server); 422 on a bad agentTime", async () => {
  const admin = await person("admin");
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const app = await enrollSite(admin, id, "app");
  const hb = {
    siteSlug: "app",
    agentVersion: "0.11.0-test",
    headRevId: null,
    staging: null,
    peersSeen: [],
    metrics: { cpu: 0, memPct: 0, diskPct: 0 },
    edge: null,
    keyVersion: 1,
    advertiseHost: "127.0.0.1",
    agentPort: 7080,
    ip: "127.0.0.1",
    hostname: "h",
  };
  // an old agent (no agentTime): no skew
  await app.api.heartbeat({ body: hb });
  let site = (await admin.api.getSetup({ deploymentId: id })).sites.find(
    (s) => s.siteSlug === "app",
  )!;
  assert.equal(site.agent!.clockSkew, null);
  // the agent clock 5 h 30 min ahead (the signature still verifies: the agent signs with the server offset)
  const ahead = new Date(Date.now() + 19800_000).toISOString();
  const t0 = Date.now();
  await app.api.heartbeat({ body: { ...hb, agentTime: ahead } });
  const setup = await admin.api.getSetup({ deploymentId: id });
  site = setup.sites.find((s) => s.siteSlug === "app")!;
  const seen = Date.parse(site.agent!.lastSeenAt);
  assert.ok(
    seen >= t0 - 1000 && seen <= Date.now() + 1000,
    `lastSeenAt is the server receive time (${site.agent!.lastSeenAt}), never the agent clock (${ahead})`,
  );
  assert.ok(
    Math.abs(site.agent!.clockSkew!.skewSec - 19800) <= 5,
    JSON.stringify(site.agent!.clockSkew),
  );
  assert.ok(
    Math.abs(Date.parse(site.agent!.clockSkew!.measuredAt) - seen) < 2000,
  );
  assert.equal(site.agent!.online, true);
  assert.match(
    clockSkewWarning(site.agent!.clockSkew!.skewSec)!,
    /^agent clock is 5 h 30 min ahead — fix NTP on the server/,
  );
  const st = await admin.api.getStatus({ deploymentId: id });
  assert.ok(
    Math.abs(
      st.sites.find((s) => s.siteSlug === "app")!.clockSkew!.skewSec - 19800,
    ) <= 5,
  );
  // the stored doc: lastSeenAt server time, clockSkew separate
  const doc = (
    await adb
      .collection("deployments")
      .doc(id)
      .collection("agents")
      .doc("app")
      .get()
  ).data()!;
  assert.equal(doc.lastSeenAt, site.agent!.lastSeenAt);
  // behind by 3 min → negative skew; an in-sync clock → |skew| small, no warning
  await app.api.heartbeat({
    body: { ...hb, agentTime: new Date(Date.now() - 180_000).toISOString() },
  });
  site = (await admin.api.getSetup({ deploymentId: id })).sites.find(
    (s) => s.siteSlug === "app",
  )!;
  assert.ok(Math.abs(site.agent!.clockSkew!.skewSec + 180) <= 5);
  assert.match(clockSkewWarning(site.agent!.clockSkew!.skewSec)!, /behind/);
  await app.api.heartbeat({
    body: { ...hb, agentTime: new Date().toISOString() },
  });
  site = (await admin.api.getSetup({ deploymentId: id })).sites.find(
    (s) => s.siteSlug === "app",
  )!;
  assert.ok(Math.abs(site.agent!.clockSkew!.skewSec) <= 5);
  assert.equal(clockSkewWarning(site.agent!.clockSkew!.skewSec), null);
  // without agentTime the last measurement is kept
  await app.api.heartbeat({ body: hb });
  assert.ok(
    (await admin.api.getSetup({ deploymentId: id })).sites.find(
      (s) => s.siteSlug === "app",
    )!.agent!.clockSkew !== null,
  );
  // bad shapes → 422
  for (const bad of [
    "yesterday",
    "2026-10-08 05:00:00",
    12345,
    "2026-13-40T99:99:99Z",
  ]) {
    const e = await rejects(
      app.api.heartbeat({ body: { ...hb, agentTime: bad as never } }),
      "invalid",
      422,
    );
    assert.match(JSON.stringify(e.details), /agentTime/);
  }
});
