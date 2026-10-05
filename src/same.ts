/**
 * Whether two JSON-shaped values are the same, regardless of the order of object keys.
 *
 * Pulumi stores object inputs and outputs with their keys sorted, so a diff that compared
 * `JSON.stringify(old.x)` with `JSON.stringify(desired)` reported a change whenever the code built
 * the object in another order: same content, permanent false drift in every preview. Arrays keep
 * their order, because there it carries meaning.
 */
export function sameJson(a: unknown, b: unknown): boolean {
  return JSON.stringify(canonical(a)) === JSON.stringify(canonical(b));
}

function canonical(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(canonical);
  if (value !== null && typeof value === 'object') {
    return Object.fromEntries(
      Object.keys(value as Record<string, unknown>).sort().map((k) => [k, canonical((value as Record<string, unknown>)[k])]),
    );
  }
  return value;
}
