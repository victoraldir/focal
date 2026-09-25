#!/usr/bin/env bash
set -euo pipefail

: "${GITHUB_REF_NAME:?GITHUB_REF_NAME is required}"
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
: "${SCOOP_BUCKET_TOKEN:?SCOOP_BUCKET_TOKEN is required}"

version="${GITHUB_REF_NAME#v}"
asset="focal_${version}_windows_amd64.zip"
checksum_file="dist/checksums.txt"
checksum="$(awk -v asset="$asset" '$2 == asset { print $1; exit }' "$checksum_file")"
if [[ -z "$checksum" ]]; then
	printf 'could not find checksum for %s in %s\n' "$asset" "$checksum_file" >&2
	exit 1
fi

manifest="$(mktemp)"
trap 'rm -f "$manifest"' EXIT
python3 - "$version" "$GITHUB_REPOSITORY" "$GITHUB_REF_NAME" "$checksum" >"$manifest" <<'PY'
import json
import sys

version, repository, tag, checksum = sys.argv[1:]
json.dump({
    "version": version,
    "architecture": {
        "64bit": {
            "url": f"https://github.com/{repository}/releases/download/{tag}/focal_{version}_windows_amd64.zip",
            "bin": "focal.exe",
            "hash": checksum,
        }
    },
    "homepage": f"https://github.com/{repository}",
    "description": "Convert photo sequences into timelapses by wrapping FFmpeg",
    "license": "MIT",
}, sys.stdout, indent=2)
print()
PY

bucket_dir="$(mktemp -d)"
trap 'rm -rf "$bucket_dir" "$manifest"' EXIT
git clone --quiet "https://x-access-token:${SCOOP_BUCKET_TOKEN}@github.com/victoraldir/scoop-bucket.git" "$bucket_dir"
if git -C "$bucket_dir" rev-parse --verify HEAD >/dev/null 2>&1; then
	git -C "$bucket_dir" switch main
else
	git -C "$bucket_dir" switch --orphan main
fi
cp "$manifest" "$bucket_dir/focal.json"
git -C "$bucket_dir" config user.name goreleaserbot
git -C "$bucket_dir" config user.email goreleaserbot@users.noreply.github.com
git -C "$bucket_dir" add focal.json
if ! git -C "$bucket_dir" diff --cached --quiet; then
	git -C "$bucket_dir" commit --quiet -m "scoop: focal ${GITHUB_REF_NAME}"
	git -C "$bucket_dir" push --quiet origin main
fi
