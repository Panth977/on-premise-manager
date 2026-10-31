/** Minimal POSIX ustar reader (regular files only; pax 'x'/'g' and GNU 'L' long names understood). */
export interface TarEntry {
  name: string;
  data: Buffer;
}
function str(b: Buffer, off: number, len: number) {
  const s = b.subarray(off, off + len);
  const z = s.indexOf(0);
  return (z >= 0 ? s.subarray(0, z) : s).toString('utf8');
}
function octal(b: Buffer, off: number, len: number) {
  const s = str(b, off, len).trim();
  return s ? parseInt(s, 8) : 0;
}
export function readTar(buf: Buffer): TarEntry[] {
  const out: TarEntry[] = [];
  let off = 0;
  let longName: string | null = null;
  let paxPath: string | null = null;
  while (off + 512 <= buf.length) {
    const h = buf.subarray(off, off + 512);
    if (h.every((x) => x === 0)) break;
    const size = octal(h, 124, 12);
    if (!Number.isFinite(size) || size < 0) throw new Error('bad tar header (size)');
    // checksum
    let sum = 0;
    for (let i = 0; i < 512; i++) sum += i >= 148 && i < 156 ? 32 : h[i]!;
    if (sum !== octal(h, 148, 8)) throw new Error('bad tar header (checksum)');
    const type = String.fromCharCode(h[156]! || 48);
    const magic = str(h, 257, 6);
    let name = str(h, 0, 100);
    if (magic.startsWith('ustar')) {
      const prefix = str(h, 345, 155);
      if (prefix) name = `${prefix}/${name}`;
    }
    const dataStart = off + 512;
    if (dataStart + size > buf.length) throw new Error('truncated tar');
    const data = buf.subarray(dataStart, dataStart + size);
    off = dataStart + Math.ceil(size / 512) * 512;
    if (type === 'L') {
      longName = data.toString('utf8').replace(/\0+$/, '');
      continue;
    }
    if (type === 'x') {
      const txt = data.toString('utf8');
      for (const line of txt.split('\n')) {
        const m = /^\d+ path=(.*)$/.exec(line);
        if (m) paxPath = m[1]!;
      }
      continue;
    }
    if (type === 'g') continue;
    if (longName) name = longName;
    if (paxPath) name = paxPath;
    longName = null;
    paxPath = null;
    if (type === '0' || type === '\0' || type === '7') out.push({ name: name.replace(/^\.\//, ''), data: Buffer.from(data) });
    else if (type === '5') continue;
    else throw new Error(`unsupported tar entry type ${type} (${name})`);
  }
  return out;
}

/** ustar writer (tests, tools). */
export function writeTar(entries: TarEntry[]): Buffer {
  const parts: Buffer[] = [];
  for (const e of entries) {
    const h = Buffer.alloc(512);
    if (Buffer.byteLength(e.name) > 100) throw new Error('name too long');
    h.write(e.name, 0, 100, 'utf8');
    h.write('0000644\0', 100);
    h.write('0000000\0', 108);
    h.write('0000000\0', 116);
    h.write(e.data.length.toString(8).padStart(11, '0') + '\0', 124);
    h.write(Math.floor(Date.now() / 1000).toString(8).padStart(11, '0') + '\0', 136);
    h.write('        ', 148);
    h.write('0', 156);
    h.write('ustar\0', 257);
    h.write('00', 263);
    let sum = 0;
    for (const x of h) sum += x;
    h.write(sum.toString(8).padStart(6, '0') + '\0 ', 148);
    parts.push(h, e.data, Buffer.alloc((512 - (e.data.length % 512)) % 512));
  }
  parts.push(Buffer.alloc(1024));
  return Buffer.concat(parts);
}
