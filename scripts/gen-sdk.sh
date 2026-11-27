#!/usr/bin/env bash
# Regenerates the TypeScript SDK in sdk/nodejs from the plugin's own schema. CI runs this and fails
# when the committed SDK differs, so run it after any change to the provider's resources or config.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
bin="$root/provider/bin/pulumi-resource-homelab"
(cd "$root/provider" && go build -o "$bin" ./cmd/pulumi-resource-homelab)
out="$(mktemp -d)"
trap 'rm -rf "$out"' EXIT
pulumi package gen-sdk "$bin" --language nodejs --out "$out"
rm -rf "$root/sdk/nodejs"
mkdir -p "$root/sdk"
mv "$out/nodejs" "$root/sdk/nodejs"
# Consumers type-check these sources with their own compiler settings, and the generated
# `isInstance` overrides a base-class member without saying so, which `noImplicitOverride` rejects.
# Saying so changes nothing at runtime and is valid under every setting.
find "$root/sdk/nodejs" -name '*.ts' -exec sed -i.bak 's/public static isInstance(/public static override isInstance(/' {} +
find "$root/sdk/nodejs" -name '*.bak' -delete
# Consumers link this directory (`link:../pulumi-homelab/sdk/nodejs`) and load it as TypeScript, the
# way they load the library's own src/index.ts, so point main at the source rather than a build.
node -e '
const f = process.argv[1], p = JSON.parse(require("fs").readFileSync(f, "utf8"));
p.main = "index.ts";
require("fs").writeFileSync(f, JSON.stringify(p, null, 4) + "\n");' "$root/sdk/nodejs/package.json"
