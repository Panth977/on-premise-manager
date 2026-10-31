import { randomBytes } from 'node:crypto';
import { argon2id, argon2Verify } from 'hash-wasm';
import { groupKey } from './util.js';

const B32 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
/** RFC 4648 base32 without padding. */
export function base32(buf: Uint8Array): string {
  let bits = 0;
  let value = 0;
  let out = '';
  for (const byte of buf) {
    value = (value << 8) | byte;
    bits += 8;
    while (bits >= 5) {
      out += B32[(value >>> (bits - 5)) & 31];
      bits -= 5;
    }
  }
  if (bits > 0) out += B32[(value << (5 - bits)) & 31];
  return out;
}
/** Normalise user input: upper-case, keep only A–Z2–7 (SPEC §4.6). */
export const normalizeAccessKey = (s: string) => s.toUpperCase().replace(/[^A-Z2-7]/g, '');

/** argon2id m=65536 KiB, t=3, p=1, 16-byte salt, 32-byte output → PHC string (std base64, no padding). */
export async function hashAccessKey(key: string): Promise<string> {
  return argon2id({
    password: normalizeAccessKey(key),
    salt: randomBytes(16),
    parallelism: 1,
    iterations: 3,
    memorySize: 65536,
    hashLength: 32,
    outputType: 'encoded',
  });
}
export async function verifyAccessKey(key: string, phc: string): Promise<boolean> {
  return argon2Verify({ password: normalizeAccessKey(key), hash: phc });
}

/** New access key: crypto.randomBytes(15) → base32 (24 chars). Returns plain (grouped for display) + PHC hash. */
export async function newAccessKey(): Promise<{ plain: string; grouped: string; hash: string }> {
  const plain = base32(randomBytes(15));
  return { plain, grouped: groupKey(plain), hash: await hashAccessKey(plain) };
}
