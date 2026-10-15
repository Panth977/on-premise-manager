#!/usr/bin/env node
/**
 * SPEC-final §21 #84 (g): no text in the built console may say "shown once", "only once", "one-time" or "I saved".
 * Usage: node scripts/check-bundle-text.mjs [dir=build]  → exit 1 with every hit, 0 when clean.
 */
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

export const FORBIDDEN = /shown once|only once|one-time|I saved/i;

/**
 * @param {string} dir
 * @returns {{ files: number; hits: { file: string; match: string; context: string }[] }}
 */
export function scanBundle(dir) {
  /** @type {{ file: string; match: string; context: string }[]} */
  const hits = [];
  let files = 0;
  /** @param {string} d */
  const walk = (d) => {
    for (const name of readdirSync(d)) {
      const p = join(d, name);
      const st = statSync(p);
      if (st.isDirectory()) walk(p);
      else {
        files++;
        const text = readFileSync(p).toString('utf8');
        const re = new RegExp(FORBIDDEN.source, 'gi');
        let m;
        while ((m = re.exec(text))) hits.push({ file: relative(dir, p), match: m[0], context: text.slice(Math.max(0, m.index - 60), m.index + 60).replace(/\s+/g, ' ') });
      }
    }
  };
  walk(dir);
  return { files, hits };
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const dir = resolve(process.argv[2] ?? 'build');
  const { files, hits } = scanBundle(dir);
  if (!files) {
    console.error(`no files in ${dir} — run npm run build first`);
    process.exit(2);
  }
  if (hits.length) {
    for (const h of hits) console.error(`${h.file}: "${h.match}" … ${h.context}`);
    console.error(`FAIL: ${hits.length} forbidden phrase(s) in ${files} files of ${dir}`);
    process.exit(1);
  }
  console.log(`ok: ${files} files in ${dir}, no /${FORBIDDEN.source}/i`);
}
