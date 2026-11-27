// The JavaScript side of jscompat's differential test. Reads a JSON request on stdin and answers
// each case with what the JavaScript built-in returns. Numbers travel as the hexadecimal bit
// pattern of the float64, so NaN, ±Infinity and -0 survive JSON.
import { readFileSync } from 'node:fs';

const req = JSON.parse(readFileSync(0, 'utf8'));
const view = new DataView(new ArrayBuffer(8));
const fromBits = (h) => { view.setBigUint64(0, BigInt('0x' + h)); return view.getFloat64(0); };
const toBits = (f) => { view.setFloat64(0, f); return view.getBigUint64(0).toString(16).padStart(16, '0'); };

const spaces = [];
for (let c = 0; c <= 0x10ffff; c++) {
  if (c >= 0xd800 && c <= 0xdfff) continue;
  if (/^\s$/u.test(String.fromCodePoint(c))) spaces.push(c);
}
// Code points this node's Unicode does not assign, as [first, last] runs: a newer Go assigns (and
// case-maps) some of them, which is a difference of Unicode versions, not of this package.
const unassigned = [];
for (let c = 0, start = -1; c <= 0x110000; c++) {
  const free = c < 0x110000 && (c < 0xd800 || c > 0xdfff) && !/\p{Assigned}/u.test(String.fromCodePoint(c));
  if (free && start < 0) start = c;
  if (!free && start >= 0) { unassigned.push([start, c - 1]); start = -1; }
}
const cases = [];
for (let c = 0; c <= 0x10ffff; c++) {
  if (c >= 0xd800 && c <= 0xdfff) continue;
  const s = String.fromCodePoint(c);
  const u = s.toUpperCase(), l = s.toLowerCase();
  if (u !== s || l !== s) cases.push([c, u, l]);
}

const res = {
  unicode: process.versions.unicode,
  unassigned,
  spaces,
  cases,
  trim: req.strings.map((s) => [s.trim(), s.trimStart(), s.trimEnd(), s.split(/\s+/)]),
  number: req.strings.map((s) => toBits(Number(s))),
  format: req.floats.map((h) => String(fromBits(h))),
  round: req.floats.map((h) => toBits(Math.round(fromBits(h)))),
  keyOrder: req.keyLists.map((keys) => {
    const o = {};
    for (const k of keys) o[k] = 1;
    return Object.keys(o);
  }),
  sortedKeys: req.keyLists.map((keys) => {
    const o = {};
    for (const k of [...keys].sort()) o[k] = 1;
    return Object.keys(o);
  }),
};
process.stdout.write(JSON.stringify(res));
