import { test } from 'node:test';
import assert from 'node:assert/strict';
import { ansi256Hex, applySgr, detectEscLost, parseAnsi, renderAnsiLines, stripAnsi } from './ansi.js';

// §21 #93: the user's Deno log line (agent 0.10.0 Tools → Logs)
const DENO = '\x1b[0m\x1b[1m\x1b[31merror\x1b[0m: Uncaught (in promise) Error: getaddrinfo EAI_AGAIN mqtt';
const AT = '    \x1b[0m\x1b[2m\x1b[38;5;245mat \x1b[0m\x1b[0m\x1b[2m\x1b[38;5;245mext:deno_node/dns.ts\x1b[0m';

test('SGR → spans: bold red "error", then plain text', () => {
  const { spans, state } = parseAnsi(DENO);
  assert.deepEqual(spans[0], { text: 'error', style: { bold: true, fg: { kind: 'idx', n: 1 } } });
  assert.deepEqual(spans[1], { text: ': Uncaught (in promise) Error: getaddrinfo EAI_AGAIN mqtt', style: {} });
  assert.deepEqual(state, {});
});

test('256 colors, dim, truecolor, bright, bg, inverse, underline, italic, strike, resets', () => {
  const { spans } = parseAnsi(AT);
  assert.deepEqual(spans[0], { text: '    ', style: {} });
  assert.deepEqual(spans[1], { text: 'at ext:deno_node/dns.ts', style: { dim: true, fg: { kind: 'idx', n: 245 } } }); // same style merged
  assert.deepEqual(applySgr({}, '38;2;255;128;0'), { fg: { kind: 'rgb', hex: '#ff8000' } });
  assert.deepEqual(applySgr({}, '48;5;21'), { bg: { kind: 'idx', n: 21 } });
  assert.deepEqual(applySgr({}, '92;104'), { fg: { kind: 'idx', n: 10 }, bg: { kind: 'idx', n: 12 } });
  assert.deepEqual(applySgr({}, '3;4;7;9'), { italic: true, underline: true, inverse: true, strike: true });
  assert.deepEqual(applySgr({ bold: true, dim: true, italic: true, underline: true, inverse: true, fg: { kind: 'idx', n: 1 }, bg: { kind: 'idx', n: 2 } }, '22;23;24;27;39;49'), {});
  assert.deepEqual(applySgr({ bold: true }, ''), {}); // ESC[m = reset
  assert.deepEqual(applySgr({}, '38:5:208'), { fg: { kind: 'idx', n: 208 } }); // colon form
  assert.equal(ansi256Hex(245), '#8a8a8a');
  assert.equal(ansi256Hex(196), '#ff0000');
  assert.equal(ansi256Hex(16), '#000000');
});

test('state carries across lines', () => {
  const a = parseAnsi('\x1b[32mgreen');
  const b = parseAnsi('still green\x1b[0m done', a.state);
  assert.deepEqual(b.spans, [{ text: 'still green', style: { fg: { kind: 'idx', n: 2 } } }, { text: ' done', style: {} }]);
});

test('non-SGR escapes are stripped, text kept (cursor, erase, OSC title, OSC 8 hyperlink, charset)', () => {
  const s = '\x1b[2K\x1b[1G\x1b]0;title\x07\x1b]8;;https://x.y\x1b\\link\x1b]8;;\x1b\\ \x1b(Bok\x1b[?25l\r';
  assert.equal(stripAnsi(s), 'link ok');
  assert.deepEqual(parseAnsi(s).spans, [{ text: 'link ok', style: {} }]);
});

test('ESC lost: detected per stream, bare [..m parsed only then; "[1]" never touched', () => {
  const lost = ['[0m[1m[31merror[0m: Uncaught (in promise) Error', '    [0m[2m[38;5;245mat [0m'];
  assert.equal(detectEscLost(lost), true);
  assert.equal(detectEscLost(['[1] first item', 'array[0] = x', 'the [m] key']), false);
  assert.equal(detectEscLost([DENO, '[0m bare']), false); // ESC present somewhere → never
  assert.equal(stripAnsi(lost[0]!, { escLost: true }), 'error: Uncaught (in promise) Error');
  assert.deepEqual(parseAnsi(lost[0]!, {}, { escLost: true }).spans[0], { text: 'error', style: { bold: true, fg: { kind: 'idx', n: 1 } } });
  assert.equal(stripAnsi('see [1] and [12] and [1m] x'), 'see [1] and [12] and [1m] x'); // ESC mode: bare stays
  assert.equal(stripAnsi('see [1] and [12]', { escLost: true }), 'see [1] and [12]');
});

test('renderAnsiLines: colors, plain, raw; HTML-escaped; styles never from input', () => {
  const [c] = renderAnsiLines(['\x1b[31m<b>x</b>\x1b[0m']);
  assert.equal(c, '<span style="color:var(--ansi-1, #cf222e)">&lt;b&gt;x&lt;/b&gt;</span>');
  assert.deepEqual(renderAnsiLines(['\x1b[31mx\x1b[0m'], { colors: false }), ['x']);
  assert.deepEqual(renderAnsiLines(['\x1b[31mx\x1b[0m'], { raw: true }), ['␛[31mx␛[0m']);
  const [d] = renderAnsiLines(['\x1b[1;38;2;1;2;3;48;5;250;4mz']);
  assert.equal(d, '<span style="color:#010203;background:#bcbcbc;font-weight:600;text-decoration:underline">z</span>');
});
