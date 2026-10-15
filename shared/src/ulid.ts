/** Minimal ULID (Crockford base32, 48-bit ms time + 80-bit randomness). Monotonic within one process. */
const ENC = '0123456789ABCDEFGHJKMNPQRSTVWXYZ';
export const ULID_RE = /^[0-9A-HJKMNP-TV-Z]{26}$/;

let lastTime = -1;
let lastRand: number[] = [];

function randomBytes(n: number): Uint8Array {
  const b = new Uint8Array(n);
  globalThis.crypto.getRandomValues(b);
  return b;
}

export function ulid(now: number = Date.now()): string {
  let rand: number[];
  if (now === lastTime) {
    rand = lastRand.slice();
    for (let i = rand.length - 1; i >= 0; i--) {
      if (rand[i]! < 31) {
        rand[i]! += 1;
        break;
      }
      rand[i] = 0;
    }
  } else {
    const b = randomBytes(16);
    rand = Array.from(b, (x) => x % 32);
  }
  lastTime = now;
  lastRand = rand;
  let t = now;
  let timePart = '';
  for (let i = 0; i < 10; i++) {
    timePart = ENC[t % 32] + timePart;
    t = Math.floor(t / 32);
  }
  return timePart + rand.map((x) => ENC[x]).join('');
}

export function ulidTime(id: string): number {
  let t = 0;
  for (let i = 0; i < 10; i++) t = t * 32 + ENC.indexOf(id[i]!);
  return t;
}
