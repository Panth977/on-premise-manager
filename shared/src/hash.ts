/**
 * Synchronous hashes for the expression filters `sha256`, `sha512` and `mosquitto_passwd` (SPEC-final §10.4, §21 #103).
 * Pure TypeScript (no WebCrypto: expressions resolve synchronously in the browser, the backend and tests). The Go
 * agent uses crypto/sha256, crypto/sha512, crypto/hmac and crypto/pbkdf2; testdata/expr-vectors.json pins both.
 *
 * Inputs are bytes (Uint8Array) — text is UTF-8 encoded by the caller (`utf8`).
 */

export const utf8 = (s: string): Uint8Array => new TextEncoder().encode(s);

export function toHex(b: Uint8Array): string {
  let s = '';
  for (const x of b) s += x.toString(16).padStart(2, '0');
  return s;
}

/** Standard Base64 with padding (RFC 4648 §4) — what OpenSSL's BIO_f_base64 writes (no newlines). */
export function toBase64(b: Uint8Array): string {
  let bin = '';
  for (const x of b) bin += String.fromCharCode(x);
  return btoa(bin);
}

export function fromBase64(s: string): Uint8Array {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

function concat(...parts: Uint8Array[]): Uint8Array {
  let n = 0;
  for (const p of parts) n += p.length;
  const out = new Uint8Array(n);
  let o = 0;
  for (const p of parts) {
    out.set(p, o);
    o += p.length;
  }
  return out;
}

// ───────────────────────────────────────────────────────────── SHA-256 (FIPS 180-4)
const K256 = new Uint32Array([
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5, 0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
  0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da, 0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
  0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85, 0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3, 0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
]);

/** Pad a message to whole blocks: 0x80, zeros, then the bit length big-endian in `lenBytes` bytes (8 or 16). */
function pad(msg: Uint8Array, block: number, lenBytes: number): Uint8Array {
  const n = Math.ceil((msg.length + 1 + lenBytes) / block) * block;
  const out = new Uint8Array(n);
  out.set(msg);
  out[msg.length] = 0x80;
  // bit length = len * 8; messages here are far below 2^53 bits
  const bits = msg.length * 8;
  const hi = Math.floor(bits / 0x100000000);
  const lo = bits >>> 0;
  const dv = new DataView(out.buffer);
  dv.setUint32(n - 8, hi);
  dv.setUint32(n - 4, lo);
  return out;
}

export function sha256(msg: Uint8Array): Uint8Array {
  const h = new Uint32Array([0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19]);
  const p = pad(msg, 64, 8);
  const dv = new DataView(p.buffer);
  const w = new Uint32Array(64);
  const rotr = (x: number, n: number) => (x >>> n) | (x << (32 - n));
  for (let off = 0; off < p.length; off += 64) {
    for (let i = 0; i < 16; i++) w[i] = dv.getUint32(off + i * 4);
    for (let i = 16; i < 64; i++) {
      const a = w[i - 15]!;
      const b = w[i - 2]!;
      const s0 = rotr(a, 7) ^ rotr(a, 18) ^ (a >>> 3);
      const s1 = rotr(b, 17) ^ rotr(b, 19) ^ (b >>> 10);
      w[i] = (w[i - 16]! + s0 + w[i - 7]! + s1) >>> 0;
    }
    let a = h[0]!, b = h[1]!, c = h[2]!, d = h[3]!, e = h[4]!, f = h[5]!, g = h[6]!, hh = h[7]!;
    for (let i = 0; i < 64; i++) {
      const S1 = rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25);
      const ch = (e & f) ^ (~e & g);
      const t1 = (hh + S1 + ch + K256[i]! + w[i]!) >>> 0;
      const S0 = rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22);
      const maj = (a & b) ^ (a & c) ^ (b & c);
      const t2 = (S0 + maj) >>> 0;
      hh = g;
      g = f;
      f = e;
      e = (d + t1) >>> 0;
      d = c;
      c = b;
      b = a;
      a = (t1 + t2) >>> 0;
    }
    h[0] = (h[0]! + a) >>> 0;
    h[1] = (h[1]! + b) >>> 0;
    h[2] = (h[2]! + c) >>> 0;
    h[3] = (h[3]! + d) >>> 0;
    h[4] = (h[4]! + e) >>> 0;
    h[5] = (h[5]! + f) >>> 0;
    h[6] = (h[6]! + g) >>> 0;
    h[7] = (h[7]! + hh) >>> 0;
  }
  const out = new Uint8Array(32);
  const ov = new DataView(out.buffer);
  for (let i = 0; i < 8; i++) ov.setUint32(i * 4, h[i]!);
  return out;
}

// ───────────────────────────────────────────────────────────── SHA-512 (FIPS 180-4), 64-bit words as hi / lo 32-bit halves
const K512 = [
  '428a2f98d728ae22', '7137449123ef65cd', 'b5c0fbcfec4d3b2f', 'e9b5dba58189dbbc', '3956c25bf348b538', '59f111f1b605d019', '923f82a4af194f9b', 'ab1c5ed5da6d8118',
  'd807aa98a3030242', '12835b0145706fbe', '243185be4ee4b28c', '550c7dc3d5ffb4e2', '72be5d74f27b896f', '80deb1fe3b1696b1', '9bdc06a725c71235', 'c19bf174cf692694',
  'e49b69c19ef14ad2', 'efbe4786384f25e3', '0fc19dc68b8cd5b5', '240ca1cc77ac9c65', '2de92c6f592b0275', '4a7484aa6ea6e483', '5cb0a9dcbd41fbd4', '76f988da831153b5',
  '983e5152ee66dfab', 'a831c66d2db43210', 'b00327c898fb213f', 'bf597fc7beef0ee4', 'c6e00bf33da88fc2', 'd5a79147930aa725', '06ca6351e003826f', '142929670a0e6e70',
  '27b70a8546d22ffc', '2e1b21385c26c926', '4d2c6dfc5ac42aed', '53380d139d95b3df', '650a73548baf63de', '766a0abb3c77b2a8', '81c2c92e47edaee6', '92722c851482353b',
  'a2bfe8a14cf10364', 'a81a664bbc423001', 'c24b8b70d0f89791', 'c76c51a30654be30', 'd192e819d6ef5218', 'd69906245565a910', 'f40e35855771202a', '106aa07032bbd1b8',
  '19a4c116b8d2d0c8', '1e376c085141ab53', '2748774cdf8eeb99', '34b0bcb5e19b48a8', '391c0cb3c5c95a63', '4ed8aa4ae3418acb', '5b9cca4f7763e373', '682e6ff3d6b2b8a3',
  '748f82ee5defb2fc', '78a5636f43172f60', '84c87814a1f0ab72', '8cc702081a6439ec', '90befffa23631e28', 'a4506cebde82bde9', 'bef9a3f7b2c67915', 'c67178f2e372532b',
  'ca273eceea26619c', 'd186b8c721c0c207', 'eada7dd6cde0eb1e', 'f57d4f7fee6ed178', '06f067aa72176fba', '0a637dc5a2c898a6', '113f9804bef90dae', '1b710b35131c471b',
  '28db77f523047d84', '32caab7b40c72493', '3c9ebe0a15c9bebc', '431d67c49c100d4c', '4cc5d4becb3e42b6', '597f299cfc657e2a', '5fcb6fab3ad6faec', '6c44198c4a475817',
];
const K512H = new Uint32Array(K512.map((x) => parseInt(x.slice(0, 8), 16)));
const K512L = new Uint32Array(K512.map((x) => parseInt(x.slice(8), 16)));
const IV512 = ['6a09e667f3bcc908', 'bb67ae8584caa73b', '3c6ef372fe94f82b', 'a54ff53a5f1d36f1', '510e527fade682d1', '9b05688c2b3e6c1f', '1f83d9abfb41bd6b', '5be0cd19137e2179'];

export function sha512(msg: Uint8Array): Uint8Array {
  const hh = new Uint32Array(IV512.map((x) => parseInt(x.slice(0, 8), 16)));
  const hl = new Uint32Array(IV512.map((x) => parseInt(x.slice(8), 16)));
  const p = pad(msg, 128, 16);
  const dv = new DataView(p.buffer);
  const wh = new Uint32Array(80);
  const wl = new Uint32Array(80);
  // 64-bit rotate right by n of (h, l): returns [hi, lo]
  const rotrH = (h: number, l: number, n: number) => (n < 32 ? (h >>> n) | (l << (32 - n)) : (l >>> (n - 32)) | (h << (64 - n)));
  const rotrL = (h: number, l: number, n: number) => (n < 32 ? (l >>> n) | (h << (32 - n)) : (h >>> (n - 32)) | (l << (64 - n)));
  const shrH = (h: number, _l: number, n: number) => h >>> n; // n < 32
  const shrL = (h: number, l: number, n: number) => (l >>> n) | (h << (32 - n));
  for (let off = 0; off < p.length; off += 128) {
    for (let i = 0; i < 16; i++) {
      wh[i] = dv.getUint32(off + i * 8);
      wl[i] = dv.getUint32(off + i * 8 + 4);
    }
    for (let i = 16; i < 80; i++) {
      const ah = wh[i - 15]!, al = wl[i - 15]!;
      const s0h = rotrH(ah, al, 1) ^ rotrH(ah, al, 8) ^ shrH(ah, al, 7);
      const s0l = rotrL(ah, al, 1) ^ rotrL(ah, al, 8) ^ shrL(ah, al, 7);
      const bh = wh[i - 2]!, bl = wl[i - 2]!;
      const s1h = rotrH(bh, bl, 19) ^ rotrH(bh, bl, 61) ^ shrH(bh, bl, 6);
      const s1l = rotrL(bh, bl, 19) ^ rotrL(bh, bl, 61) ^ shrL(bh, bl, 6);
      // w = w[i-16] + s0 + w[i-7] + s1
      let lo = (wl[i - 16]! >>> 0) + (s0l >>> 0) + (wl[i - 7]! >>> 0) + (s1l >>> 0);
      let hi = wh[i - 16]! + s0h + wh[i - 7]! + s1h + Math.floor(lo / 0x100000000);
      wl[i] = lo >>> 0;
      wh[i] = hi >>> 0;
    }
    let ah = hh[0]!, al = hl[0]!, bh = hh[1]!, bl = hl[1]!, ch = hh[2]!, cl = hl[2]!, dh = hh[3]!, dl = hl[3]!;
    let eh = hh[4]!, el = hl[4]!, fh = hh[5]!, fl = hl[5]!, gh = hh[6]!, gl = hl[6]!, xh = hh[7]!, xl = hl[7]!;
    for (let i = 0; i < 80; i++) {
      const S1h = rotrH(eh, el, 14) ^ rotrH(eh, el, 18) ^ rotrH(eh, el, 41);
      const S1l = rotrL(eh, el, 14) ^ rotrL(eh, el, 18) ^ rotrL(eh, el, 41);
      const chh = (eh & fh) ^ (~eh & gh);
      const chl = (el & fl) ^ (~el & gl);
      // t1 = x + S1 + ch + K + w
      const t1l = (xl >>> 0) + (S1l >>> 0) + (chl >>> 0) + K512L[i]! + wl[i]!;
      const t1h = (xh + S1h + chh + K512H[i]! + wh[i]! + Math.floor(t1l / 0x100000000)) >>> 0;
      const S0h = rotrH(ah, al, 28) ^ rotrH(ah, al, 34) ^ rotrH(ah, al, 39);
      const S0l = rotrL(ah, al, 28) ^ rotrL(ah, al, 34) ^ rotrL(ah, al, 39);
      const mjh = (ah & bh) ^ (ah & ch) ^ (bh & ch);
      const mjl = (al & bl) ^ (al & cl) ^ (bl & cl);
      const t2l = (S0l >>> 0) + (mjl >>> 0);
      const t2h = (S0h + mjh + Math.floor(t2l / 0x100000000)) >>> 0;
      const t1lo = t1l >>> 0;
      const t2lo = t2l >>> 0;
      xh = gh;
      xl = gl;
      gh = fh;
      gl = fl;
      fh = eh;
      fl = el;
      const el2 = dl + t1lo;
      eh = (dh + t1h + Math.floor(el2 / 0x100000000)) >>> 0;
      el = el2 >>> 0;
      dh = ch;
      dl = cl;
      ch = bh;
      cl = bl;
      bh = ah;
      bl = al;
      const al2 = t1lo + t2lo;
      ah = (t1h + t2h + Math.floor(al2 / 0x100000000)) >>> 0;
      al = al2 >>> 0;
    }
    const add = (i: number, h: number, l: number) => {
      const lo = hl[i]! + (l >>> 0);
      hh[i] = (hh[i]! + h + Math.floor(lo / 0x100000000)) >>> 0;
      hl[i] = lo >>> 0;
    };
    add(0, ah, al);
    add(1, bh, bl);
    add(2, ch, cl);
    add(3, dh, dl);
    add(4, eh, el);
    add(5, fh, fl);
    add(6, gh, gl);
    add(7, xh, xl);
  }
  const out = new Uint8Array(64);
  const ov = new DataView(out.buffer);
  for (let i = 0; i < 8; i++) {
    ov.setUint32(i * 8, hh[i]!);
    ov.setUint32(i * 8 + 4, hl[i]!);
  }
  return out;
}

// ───────────────────────────────────────────────────────────── HMAC (RFC 2104) + PBKDF2 (RFC 8018)
type Hash = { fn: (m: Uint8Array) => Uint8Array; block: number; size: number };
export const SHA256: Hash = { fn: sha256, block: 64, size: 32 };
export const SHA512: Hash = { fn: sha512, block: 128, size: 64 };

export function hmac(h: Hash, key: Uint8Array, msg: Uint8Array): Uint8Array {
  const k = key.length > h.block ? h.fn(key) : key;
  const kp = new Uint8Array(h.block);
  kp.set(k);
  const ipad = kp.map((b) => b ^ 0x36);
  const opad = kp.map((b) => b ^ 0x5c);
  return h.fn(concat(opad, h.fn(concat(ipad, msg))));
}

export function pbkdf2(h: Hash, password: Uint8Array, salt: Uint8Array, iterations: number, keyLen: number): Uint8Array {
  const out = new Uint8Array(keyLen);
  // precompute the padded keys once (HMAC with a fixed key)
  const k = password.length > h.block ? h.fn(password) : password;
  const kp = new Uint8Array(h.block);
  kp.set(k);
  const ipad = kp.map((b) => b ^ 0x36);
  const opad = kp.map((b) => b ^ 0x5c);
  const mac = (m: Uint8Array) => h.fn(concat(opad, h.fn(concat(ipad, m))));
  for (let block = 1, off = 0; off < keyLen; block++, off += h.size) {
    const idx = new Uint8Array([(block >>> 24) & 0xff, (block >>> 16) & 0xff, (block >>> 8) & 0xff, block & 0xff]);
    let u = mac(concat(salt, idx));
    const t = u.slice();
    for (let i = 1; i < iterations; i++) {
      u = mac(u);
      for (let j = 0; j < t.length; j++) t[j]! ^= u[j]!;
    }
    out.set(t.subarray(0, Math.min(h.size, keyLen - off)), off);
  }
  return out;
}

// ───────────────────────────────────────────────────────────── Mosquitto password file lines (§21 #103)
/**
 * Mosquitto `mosquitto_passwd` hash (src/password_mosq.c, apps/mosquitto_passwd/mosquitto_passwd.c):
 *   format 7 (2.x default, sha512-pbkdf2): `$7$<iterations>$<base64 salt>$<base64 PBKDF2-HMAC-SHA512(password, salt, iterations, 64)>`
 *   format 6 (1.x, sha512):                `$6$<base64 salt>$<base64 SHA512(password ‖ salt)>`
 * salt = 12 bytes (SALT_LEN), base64 = standard with padding, no newline (BIO_FLAGS_BASE64_NO_NL). The password is
 * hashed up to its first NUL byte (strlen) — callers refuse a NUL.
 */
export const MOSQUITTO_SALT_LEN = 12;
export const MOSQUITTO_DEFAULT_ITERATIONS = 101;
export const MOSQUITTO_MAX_ITERATIONS = 10000;
export function mosquittoHash(password: string, salt: Uint8Array, format: 6 | 7 = 7, iterations = MOSQUITTO_DEFAULT_ITERATIONS): string {
  const pw = utf8(password);
  if (format === 6) return `$6$${toBase64(salt)}$${toBase64(sha512(concat(pw, salt)))}`;
  return `$7$${iterations}$${toBase64(salt)}$${toBase64(pbkdf2(SHA512, pw, salt, iterations, 64))}`;
}
/**
 * The DETERMINISTIC salt of the `mosquitto_passwd` filter: the first 12 bytes of
 * HMAC-SHA256(key = password, msg = "opm-mosquitto-salt" NUL deploymentId NUL siteSlug).
 * Same password + same site → the same line (the rendered file is stable: no hash churn, no restart on apply);
 * the deployment id + site slug make lines differ between sites / deployments, so a table precomputed for one
 * site does not apply to another. The password keys the HMAC, so the salt reveals nothing the hash does not.
 */
export function mosquittoSalt(password: string, deploymentId: string, siteSlug: string): Uint8Array {
  return hmac(SHA256, utf8(password), utf8(`opm-mosquitto-salt\u0000${deploymentId}\u0000${siteSlug}`)).slice(0, MOSQUITTO_SALT_LEN);
}
