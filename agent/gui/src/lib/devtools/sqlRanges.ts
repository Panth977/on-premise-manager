/**
 * Position-aware statement split (the GUI needs offsets for "run the statement under the cursor" and to place an error
 * position inside the editor). Same lexical rules as shared `splitSqlStatements` (";" outside '…', "…", `…`,
 * $tag$…$tag$, -- and /* *\/ comments); shared drops positions, so this mirrors it and keeps them.
 * Used by the main bundle AND the lazily loaded CodeMirror bundle (no imports).
 */
export interface StmtRange {
  /** offset of the first non-blank character */
  from: number;
  /** offset after the last non-blank character (before the ";") */
  to: number;
  /** offset of the terminating ";" (or text length) — the cursor "belongs" to this statement up to here */
  end: number;
  text: string;
}

function onlyComments(s: string): boolean {
  let i = 0;
  const n = s.length;
  while (i < n) {
    const ch = s[i]!;
    if (/\s/.test(ch)) {
      i++;
      continue;
    }
    if (ch === '-' && s[i + 1] === '-') {
      const e = s.indexOf('\n', i);
      i = e < 0 ? n : e;
      continue;
    }
    if (ch === '/' && s[i + 1] === '*') {
      const e = s.indexOf('*/', i + 2);
      i = e < 0 ? n : e + 2;
      continue;
    }
    return false;
  }
  return true;
}

export function sqlStatementRanges(text: string): StmtRange[] {
  const out: StmtRange[] = [];
  let start = 0;
  let i = 0;
  const n = text.length;
  const push = (endAt: number) => {
    const raw = text.slice(start, endAt);
    if (!onlyComments(raw)) {
      const lead = raw.length - raw.trimStart().length;
      const t = raw.trim();
      out.push({ from: start + lead, to: start + lead + t.length, end: endAt, text: t });
    }
  };
  while (i < n) {
    const ch = text[i]!;
    const nx = text[i + 1];
    if (ch === '-' && nx === '-') {
      const e = text.indexOf('\n', i);
      i = e < 0 ? n : e;
      continue;
    }
    if (ch === '/' && nx === '*') {
      const e = text.indexOf('*/', i + 2);
      i = e < 0 ? n : e + 2;
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
      i = Math.min(n, j + 1);
      continue;
    }
    if (ch === '$') {
      const m = /^\$([A-Za-z_][A-Za-z0-9_]*)?\$/.exec(text.slice(i, i + 64));
      if (m && !(i > 0 && /[A-Za-z0-9_]/.test(text[i - 1]!))) {
        const e = text.indexOf(m[0], i + m[0].length);
        i = e < 0 ? n : e + m[0].length;
        continue;
      }
    }
    if (ch === ';') {
      push(i);
      start = i + 1;
      i++;
      continue;
    }
    i++;
  }
  push(n);
  return out;
}

/** the statement the cursor is in (or right after its ";"); between statements → the previous one */
export function statementAt(text: string, pos: number): StmtRange | null {
  const rs = sqlStatementRanges(text);
  if (!rs.length) return null;
  for (const r of rs) if (pos <= r.end) return pos < r.from && r !== rs[0] ? (rs[rs.indexOf(r) - 1] ?? r) : r;
  return rs[rs.length - 1]!;
}
