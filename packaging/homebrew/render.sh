#!/usr/bin/env bash
set -euo pipefail

tag="${1:?usage: render.sh <tag>}"
ver="${tag#v}"
repo="${REPO:-scaffoldly/tush}"
binary="${BINARY:-tush}"
template="${TEMPLATE:-formula.rb.tmpl}"
here="$(cd "$(dirname "$0")" && pwd)"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

archive_sha() {
  gh release download "$tag" -R "$repo" \
    --pattern "${binary}_${1}.zip" --dir "$tmp" --clobber >/dev/null
  sha256sum "$tmp/${binary}_${1}.zip" | cut -d' ' -f1
}

sed -e "s/@@VERSION@@/${ver}/g" \
  -e "s/@@SHA_DARWIN_ARM64@@/$(archive_sha darwin_arm64)/g" \
  -e "s/@@SHA_DARWIN_AMD64@@/$(archive_sha darwin_amd64)/g" \
  -e "s/@@SHA_LINUX_ARM64@@/$(archive_sha linux_arm64)/g" \
  -e "s/@@SHA_LINUX_AMD64@@/$(archive_sha linux_amd64)/g" \
  "$here/$template"
