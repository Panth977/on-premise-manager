/** Seeded random type / value mutations of a config (shared by the normalizer vectors and the validateConfig differential). */
export function rng(seed: number) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}
type Slot = { o: any; k: string | number };
function slots(v: unknown, out: Slot[]) {
  if (Array.isArray(v)) v.forEach((x, i) => (out.push({ o: v, k: i }), slots(x, out)));
  else if (v && typeof v === 'object') for (const k of Object.keys(v)) (out.push({ o: v, k }), slots((v as any)[k], out));
}
/** swaps that keep the JSON type: wrong enums, bad slugs, oversize strings */
const WELL_TYPED_STR = ['', ' ', 'X', 'a', 'Bad Slug', 'nope', 'new', 'files', '-x', 'a'.repeat(41), 'x'.repeat(5000), '/', '/a b', '../x', 'host.docker.internal', '10.0.0.1', '{{ var.nope }}', '{{ var.', '{{ secret.x }}', '{{ container.env.NOPE }}', 'http', 'tcp', 'udp', 'tree', 'single', 'none', 'volume', 'file', 'host', 'secret', 'ü😀', 'never', 'always', 'on-failure'];
const WELL_TYPED_NUM = [0, -1, 1, 79, 80, 443, 1023, 1024, 65535, 65536, 1.5, 1e21, 7080, 8080];
const JUNK = [null, 'x', 1, 0, true, false, [], {}, [1], [null], ['x'], { a: 1 }, -1, 1.5, ''];
export type Mutation = 'typed' | 'any';
export function mutate<T>(base: T, seed: number, kind: Mutation, count = 0): T {
  const c = structuredClone(base);
  const r = rng(seed);
  const n = count || 1 + Math.floor(r() * 3);
  for (let i = 0; i < n; i++) {
    const s: Slot[] = [];
    slots(c, s);
    const slot = s[Math.floor(r() * s.length)]!;
    const v = slot.o[slot.k];
    const pick = <X,>(l: readonly X[]) => l[Math.floor(r() * l.length)]!;
    if (kind === 'typed') {
      if (typeof v === 'string') slot.o[slot.k] = pick(WELL_TYPED_STR);
      else if (typeof v === 'number') slot.o[slot.k] = pick(WELL_TYPED_NUM);
      else if (typeof v === 'boolean') slot.o[slot.k] = !v;
      else if (Array.isArray(v) && v.length) {
        const q = r();
        if (q < 0.4) v.splice(Math.floor(r() * v.length), 1);
        else if (q < 0.8) v.push(structuredClone(pick(v)));
        else slot.o[slot.k] = [];
      }
      continue;
    }
    const q = r();
    if (q < 0.25) {
      if (Array.isArray(slot.o)) slot.o.splice(slot.k as number, 1);
      else delete slot.o[slot.k];
    } else if (q < 0.5) slot.o[slot.k] = null;
    else if (q < 0.75 && typeof v === 'string') slot.o[slot.k] = pick([1, 0, true, null, [], {}, ...WELL_TYPED_NUM]);
    else if (q < 0.85 && typeof v === 'number') slot.o[slot.k] = pick(['1', '', 'x', null, true, [], {}]);
    else slot.o[slot.k] = pick(JUNK);
  }
  return c;
}
