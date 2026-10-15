import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { checkExpr, resolve, resolveNumberField, resolveContainer, ExprError, type ExprContext, type ExprField, type EnvSource, type MountSource, exprRefs, isExpr } from './expr.js';

const V = JSON.parse(readFileSync(fileURLToPath(new URL('../testdata/expr-vectors.json', import.meta.url)), 'utf8')) as {
  contexts: Record<string, ExprContext>;
  cases: { name: string; field: ExprField; ctx: string; input: string; output?: string; number?: number; masked?: string; secret?: boolean; refs?: string[]; error?: { code: string; pos: number } }[];
  check: { name: string; field: ExprField; input: string; error: { code: string; pos: number } | null }[];
  containers: {
    name: string;
    ctx: string;
    env: EnvSource[];
    mounts: MountSource[];
    expect: Record<'env' | 'mounts', Record<string, { value: string; masked: string; secret: boolean } | { error: { code: string; pos: number } }>>;
  }[];
};

for (const c of V.containers) {
  test(`resolveContainer vector: ${c.name}`, () => {
    const r = resolveContainer(V.contexts[c.ctx]!, c.env, c.mounts);
    for (const kind of ['env', 'mounts'] as const) {
      assert.deepEqual(Object.keys(r[kind]).sort(), Object.keys(c.expect[kind]).sort(), `${kind} keys`);
      for (const [k, want] of Object.entries(c.expect[kind])) {
        const got = r[kind][k]!;
        if ('error' in want) {
          assert.equal(got.ok, false, `${kind} ${k} should fail`);
          if (!got.ok) assert.deepEqual({ code: got.error.code, pos: got.error.pos }, want.error, `${kind} ${k}`);
        } else {
          assert.equal(got.ok, true, `${kind} ${k}: ${got.ok ? '' : got.error.message}`);
          if (got.ok) assert.deepEqual({ value: got.result.value, masked: got.result.masked, secret: got.result.secret }, want, `${kind} ${k}`);
        }
      }
    }
  });
}

for (const c of V.cases) {
  test(`expr vector: ${c.name}`, () => {
    const ctx = V.contexts[c.ctx]!;
    if (c.error) {
      assert.throws(
        () => resolve(c.input, ctx, c.field),
        (e: unknown) => e instanceof ExprError && e.code === c.error!.code && e.pos === c.error!.pos,
      );
      return;
    }
    const r = resolve(c.input, ctx, c.field);
    assert.equal(r.value, c.output);
    assert.equal(r.masked, c.masked);
    assert.equal(r.secret, c.secret);
    assert.deepEqual(r.refs, c.refs);
    if (c.number !== undefined) assert.equal(r.number, c.number);
    else assert.equal(r.number, null);
  });
}

for (const c of V.check) {
  test(`checkExpr vector: ${c.name}`, () => {
    const errs = checkExpr(c.input, c.field);
    if (!c.error) assert.equal(errs.length, 0);
    else {
      assert.equal(errs.length, 1);
      assert.equal(errs[0]!.code, c.error.code);
      assert.equal(errs[0]!.pos, c.error.pos);
    }
  });
}

test('resolveNumberField', () => {
  const ctx = V.contexts['app-api']!;
  assert.equal(resolveNumberField(5432, ctx, 'hostPort'), 5432);
  assert.equal(resolveNumberField(null, ctx, 'hostPort'), null);
  assert.equal(resolveNumberField('{{ var.api-port }}', ctx, 'hostPort'), 8080);
  assert.throws(() => resolveNumberField(0, ctx, 'hostPort'), (e: unknown) => e instanceof ExprError && e.code === 'E_TYPE');
  assert.throws(() => resolveNumberField('8080', ctx, 'imagePort'), (e: unknown) => e instanceof ExprError && e.code === 'E_TYPE');
});

test('exprRefs + isExpr', () => {
  assert.deepEqual(exprRefs('a{{ var.x }}b{{ "lit" }}{{ site.db.host | upper }}'), ['var.x', 'site.db.host']);
  assert.equal(isExpr('plain'), false);
  assert.equal(isExpr('{{ var.x }}'), true);
});
