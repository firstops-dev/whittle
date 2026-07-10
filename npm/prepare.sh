#!/bin/sh
# prepare.sh <version> [--publish]
# Builds the npm packages for a tagged release: downloads the goreleaser assets,
# verifies checksums, assembles 4 platform packages + the meta package under
# npm/dist/, and (with --publish) publishes all five. Version = the git tag
# without the v prefix, so npm always mirrors a real GitHub release.
set -eu
SCOPE="@firstops" # keep in sync with npm/bin/whittle.js
V="${1:?usage: prepare.sh <version, e.g. 0.3.0> [--publish]}"
PUBLISH="${2:-}"
cd "$(dirname "$0")"

work=$(mktemp -d) && trap 'rm -rf "$work"' EXIT
echo "downloading release v$V assets..."
gh release download "v$V" -R firstops-dev/whittle -p '*.tar.gz' -p checksums.txt -D "$work"
(cd "$work" && grep '\.tar\.gz' checksums.txt | shasum -a 256 -c - >/dev/null) && echo "checksums verified"

rm -rf dist && mkdir -p dist
for plat in darwin_arm64 darwin_amd64 linux_arm64 linux_amd64; do
  os="${plat%_*}"; arch="${plat#*_}"
  pkg="dist/whittle-$os-$arch"
  mkdir -p "$pkg/bin"
  tar -xzf "$work/whittle_${V}_${plat}.tar.gz" -C "$pkg/bin" whittle
  chmod +x "$pkg/bin/whittle"
  cat > "$pkg/package.json" <<EOF
{
  "name": "$SCOPE/whittle-$os-$arch",
  "version": "$V",
  "description": "whittle prebuilt binary for $os/$arch (install $SCOPE/whittle instead)",
  "license": "Apache-2.0",
  "repository": { "type": "git", "url": "git+https://github.com/firstops-dev/whittle.git" },
  "os": ["$os"],
  "cpu": ["$([ "$arch" = amd64 ] && echo x64 || echo arm64)"],
  "files": ["bin/whittle"]
}
EOF
done

mkdir -p dist/whittle/bin
sed -e "s/0\.0\.0-managed-by-prepare-sh/$V/g" package.json > dist/whittle/package.json
cp bin/whittle.js dist/whittle/bin/whittle.js && chmod +x dist/whittle/bin/whittle.js
cp README.md dist/whittle/README.md
echo "assembled: $(ls dist)"

if [ "$PUBLISH" = "--publish" ]; then
  for d in dist/whittle-*; do (cd "$d" && npm publish --access public); done
  (cd dist/whittle && npm publish --access public)
  echo "published v$V (platform packages first, meta last)"
else
  echo "dry run complete. To publish: sh npm/prepare.sh $V --publish  (requires npm login)"
fi
