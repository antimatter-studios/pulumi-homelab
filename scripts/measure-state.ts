/**
 * Measures a Pulumi state for the plugin migration's accounting (pulumi-homelab#32, rows on #31).
 *
 *   pulumi stack export --file state.json
 *   node --experimental-strip-types scripts/measure-state.ts state.json [--json]
 *   pulumi stack export | node --experimental-strip-types scripts/measure-state.ts - --json
 *
 * Reads an export (or the backend's raw `checkpoint` file) and nothing else: it never contacts
 * Pulumi, Garage or any machine. Definitions match the baseline on #31:
 * - `__provider` bytes: compact-JSON length of every `__provider` key, colon and value, in inputs
 *   and in outputs;
 * - dynamic: type starts with `pulumi-nodejs:dynamic`; plugin: `homelab:*` or `homelabk3s:*` (the k3s plugin's package name);
 * - distinct blobs: distinct `__provider` values across the whole state.
 */
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const PROVIDER = '__provider';

export interface StateMeasure {
  bytes: number;
  provider_bytes: number;
  provider_share: number;
  provider_copies: number;
  provider_distinct_blobs: number;
  resources: number;
  dynamic: number;
  plugin: number;
  other: number;
  dynamic_types: number;
}

interface Resource {
  type: string;
  custom?: boolean;
  inputs?: Record<string, unknown>;
  outputs?: Record<string, unknown>;
}

const jlen = (v: unknown): number => Buffer.byteLength(JSON.stringify(v));

/** Key-order-independent identity, so two equal objects count as one blob. */
function canonical(v: unknown): string {
  if (Array.isArray(v)) return `[${v.map(canonical).join(',')}]`;
  if (v && typeof v === 'object') {
    const o = v as Record<string, unknown>;
    return `{${Object.keys(o).sort().map((k) => `${JSON.stringify(k)}:${canonical(o[k])}`).join(',')}}`;
  }
  return JSON.stringify(v);
}

export function measureState(raw: string): StateMeasure {
  const doc = JSON.parse(raw);
  const dep = doc.checkpoint ? doc.checkpoint.latest : doc.deployment;
  const resources: Resource[] = dep?.resources ?? [];
  let providerBytes = 0;
  let copies = 0;
  const blobs = new Set<string>();
  const dynamicTypes = new Set<string>();
  let dynamic = 0;
  let plugin = 0;
  for (const r of resources) {
    if (r.custom && r.type.startsWith('pulumi-nodejs:dynamic')) {
      dynamic++;
      dynamicTypes.add(r.type);
    } else if (r.custom && (r.type.startsWith('homelab:') || r.type.startsWith('homelabk3s:'))) {
      plugin++;
    }
    for (const side of [r.inputs, r.outputs]) {
      if (side && PROVIDER in side) {
        providerBytes += jlen(side[PROVIDER]) + jlen(PROVIDER) + 1;
        copies++;
        blobs.add(typeof side[PROVIDER] === 'string' ? (side[PROVIDER] as string) : canonical(side[PROVIDER]));
      }
    }
  }
  const bytes = Buffer.byteLength(raw);
  return {
    bytes,
    provider_bytes: providerBytes,
    provider_share: bytes ? Math.round((providerBytes / bytes) * 1000) / 1000 : 0,
    provider_copies: copies,
    provider_distinct_blobs: blobs.size,
    resources: resources.length,
    dynamic,
    plugin,
    other: resources.length - dynamic - plugin,
    dynamic_types: dynamicTypes.size,
  };
}

export function table(m: StateMeasure): string {
  const n = (x: number) => x.toLocaleString('en-US');
  const rows: [string, string][] = [
    ['State size', `${n(m.bytes)} B`],
    ['__provider bytes', `${n(m.provider_bytes)} B (${(m.provider_share * 100).toFixed(1)} %)`],
    ['__provider copies', n(m.provider_copies)],
    ['__provider distinct blobs', n(m.provider_distinct_blobs)],
    ['Resources', n(m.resources)],
    ['  dynamic (TypeScript library)', `${n(m.dynamic)} (${m.dynamic_types} types)`],
    ['  plugin (homelab:*)', n(m.plugin)],
    ['  other (providers, components, other plugins)', n(m.other)],
  ];
  const w = Math.max(...rows.map(([k]) => k.length));
  return rows.map(([k, v]) => `${k.padEnd(w)}  ${v}`).join('\n');
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const args = process.argv.slice(2);
  const json = args.includes('--json');
  const path = args.find((a) => a !== '--json') ?? '-';
  const raw = readFileSync(path === '-' ? 0 : path, 'utf8');
  const m = measureState(raw);
  console.log(json ? JSON.stringify(m) : table(m));
}
