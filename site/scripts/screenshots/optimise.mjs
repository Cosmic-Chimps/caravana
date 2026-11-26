// Optimises the captures of capture.mjs: out/raw/*.png -> out/final/*.png, as 256-colour palette
// PNGs (the console is flat colour and text; no dither). Prints a table and fails the run when a
// file is over 250 KB or the set over 16 MB, so a screen that grew (or a format that stopped
// paying) is noticed. When capture.mjs listed the names it was asked for (a full run), every
// name must have both themes.
import sharp from 'sharp';
import { existsSync, mkdirSync, readFileSync, readdirSync, rmSync, statSync } from 'node:fs';

const MAX_FILE = 250 * 1024;
const MAX_SET = 16 * 1024 * 1024;
const rawDir = process.env.RAW_DIR ?? 'out/raw';
const finalDir = process.env.FINAL_DIR ?? 'out/final';
rmSync(finalDir, { recursive: true, force: true });
mkdirSync(finalDir, { recursive: true });

const files = readdirSync(rawDir).filter((f) => f.endsWith('.png')).sort();
if (files.length === 0) throw new Error(`no captures in ${rawDir}`);
if (existsSync(`${rawDir}/_names.txt`)) {
  const names = readFileSync(`${rawDir}/_names.txt`, 'utf8').split('\n').filter(Boolean);
  const missing = names.flatMap((n) => ['light', 'dark'].map((t) => `${n}-${t}.png`)).filter((f) => !files.includes(f));
  if (missing.length > 0) throw new Error(`missing captures: ${missing.join(', ')}`);
}

const rows = [];
for (const file of files) {
  const info = await sharp(`${rawDir}/${file}`)
    .png({ palette: true, colours: 256, dither: 0, compressionLevel: 9, effort: 10 })
    .toFile(`${finalDir}/${file}`);
  rows.push({ name: file, size: `${info.width}x${info.height}`, before: statSync(`${rawDir}/${file}`).size, after: info.size });
}

const kb = (bytes) => `${(bytes / 1024).toFixed(1)} KB`;
const pad = (text, width) => String(text).padEnd(width);
console.log(`${pad('name', 34)}${pad('pixels', 12)}${pad('before', 12)}after`);
for (const r of rows) console.log(`${pad(r.name, 34)}${pad(r.size, 12)}${pad(kb(r.before), 12)}${kb(r.after)}`);
const total = rows.reduce((sum, r) => sum + r.after, 0);
const largest = rows.reduce((a, b) => (b.after > a.after ? b : a));
console.log(`${pad('total', 46)}${kb(total)} in ${rows.length} files; largest ${largest.name} (${kb(largest.after)})`);

const over = rows.filter((r) => r.after > MAX_FILE);
if (over.length > 0) {
  console.error(`over ${kb(MAX_FILE)}: ${over.map((r) => `${r.name} (${kb(r.after)})`).join(', ')}`);
  process.exit(1);
}
if (total > MAX_SET) {
  console.error(`the set is ${kb(total)}, over ${kb(MAX_SET)}`);
  process.exit(1);
}
