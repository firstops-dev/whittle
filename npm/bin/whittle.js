#!/usr/bin/env node
// npx/npm shim: resolves the platform binary package and execs the real whittle.
// The Go binary is unaware this wrapper exists; go install and brew are
// unaffected alternative channels.
"use strict";
const { spawnSync } = require("child_process");

const SCOPE = "@firstops"; // keep in sync with npm/prepare.sh
const goArch = { x64: "amd64", arm64: "arm64" }[process.arch];
const goOS = { darwin: "darwin", linux: "linux" }[process.platform];

if (!goOS || !goArch) {
  console.error(
    `whittle: no prebuilt binary for ${process.platform}-${process.arch}.\n` +
      `Install from source instead:\n` +
      `  go install github.com/firstops-dev/whittle/cmd/whittle@latest\n` +
      `or download a release: https://github.com/firstops-dev/whittle/releases`
  );
  process.exit(1);
}

let bin;
try {
  bin = require.resolve(`${SCOPE}/whittle-${goOS}-${goArch}/bin/whittle`);
} catch {
  console.error(
    `whittle: platform package ${SCOPE}/whittle-${goOS}-${goArch} is not installed.\n` +
      `This usually means npm skipped optional dependencies. Try:\n` +
      `  npm install --include=optional\n` +
      `or use: go install github.com/firstops-dev/whittle/cmd/whittle@latest`
  );
  process.exit(1);
}

const r = spawnSync(bin, process.argv.slice(2), { stdio: "inherit" });
if (r.error) {
  console.error(`whittle: failed to start binary: ${r.error.message}`);
  process.exit(1);
}
process.exit(r.status === null ? 1 : r.status);
