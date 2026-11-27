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
# Consumers link this directory (`link:../pulumi-homelab/sdk/nodejs`) and load it from their
# programs, which Node runs with type stripping: that takes only erasable TypeScript, and the
# generated SDK is CommonJS-style TypeScript written to be compiled (require, exports,
# extensionless imports, <any> casts). So it is compiled here, with its own build script, and main
# points at the JavaScript; bin/ is committed so a link needs no build step.
node -e '
const f = process.argv[1], p = JSON.parse(require("fs").readFileSync(f, "utf8"));
// The generator leaves the version as the literal "${VERSION}", which the SDK then registers its
// provider with and Pulumi cannot resolve. The committed SDK is the development build, matched by
// the plugin consumers build from this checkout and install as 0.0.0-dev; a consumer that wants a
// release pins it with the version option of the provider resource.
p.version = "0.0.0-dev";
if (p.pulumi) p.pulumi.version = "0.0.0-dev";
p.main = "bin/index.js";
p.types = "bin/index.d.ts";
require("fs").writeFileSync(f, JSON.stringify(p, null, 4) + "\n");' "$root/sdk/nodejs/package.json"
(
  cd "$root/sdk/nodejs"
  npm install --no-audit --no-fund --no-package-lock --silent >/dev/null
  npx --no-install tsc
  rm -rf node_modules
  # the compiled utilities read their version from ./package.json beside them, as in Pulumi's own
  # SDK builds, which publish bin/ as the package
  node -e '
const p = JSON.parse(require("fs").readFileSync("package.json", "utf8"));
p.main = "index.js"; p.types = "index.d.ts";
require("fs").writeFileSync("bin/package.json", JSON.stringify(p, null, 4) + "\n");'

)
