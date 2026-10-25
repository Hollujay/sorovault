#!/usr/bin/env bash
# Review the dependency changes a pull request makes, without the repository's
# dependency graph.
#
# Usage: dependency-review.sh BASE HEAD CONFIG
#
# Run from the root of the repository, with BASE and HEAD naming commits it
# has, and CONFIG the licence policy to apply.
#
# GitHub's dependency-review action answers the question "does this pull
# request add a dependency the project does not accept?" from the
# repository's dependency graph. The organisation has that graph switched off,
# and the action fails with a 403 while it is off. This script answers the
# same question from the manifests the repository keeps:
#
#   1. The Go modules the pull request adds or bumps, read from go.mod.
#   2. The npm packages the pull request adds or bumps, read from the
#      package.json files in the repository.
#
# Only what the pull request changes is looked at. A dependency it does not
# touch keeps the licence it came with: the same rule the action applies, and
# for the same reason: a licence that was already there is somebody else's
# problem.
#
# A module's licence is the one its LICENSE file carries, read with
# github.com/google/go-licenses; a package's is the one the npm registry
# states. A dependency whose licence cannot be read is reported and let
# through, the way the action reports an unlicensed package and carries on.
#
# The Go side is the one that decides what SoroVault ships, so it is the one
# that is reviewed in full: every module the pull request touches, with the
# licences of all of its packages. The npm side is development scope, and
# the repository keeps no lock file for it, so only the packages a pull
# request names directly are reviewed.
#
# Exit status: 0 every licence is one the project accepts, 1 a licence is not
# accepted, 3 the review could not be carried out.
set -uo pipefail

# The comparisons below sort and compare text, so they are given a fixed
# collation rather than the one of whichever machine is running them.
export LC_ALL=C

base="${1:?usage: dependency-review.sh BASE HEAD CONFIG}"
head="${2:?usage: dependency-review.sh BASE HEAD CONFIG}"
config="${3:?usage: dependency-review.sh BASE HEAD CONFIG}"

for tool in git go node npm; do
  command -v "$tool" >/dev/null || {
    echo "dependency-review: $tool is required" >&2
    exit 3
  }
done
git cat-file -e "$base^{commit}" 2>/dev/null || {
  echo "dependency-review: unknown base $base" >&2
  exit 3
}
git cat-file -e "$head^{commit}" 2>/dev/null || {
  echo "dependency-review: unknown head $head" >&2
  exit 3
}
[ -f "$config" ] || {
  echo "dependency-review: no such policy file: $config" >&2
  exit 3
}

# The review reads the repository: the Go modules come from the module in the
# root of it, so the review runs there whatever directory it was started in.
root=$(git rev-parse --show-toplevel) || {
  echo "dependency-review: not inside a repository" >&2
  exit 3
}
config=$(cd "$(dirname "$config")" && pwd)/$(basename "$config")
cd "$root" || exit 3

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# The licences the project accepts, one identifier per line.
awk '
  /^allow-licenses:/ { list = 1; next }
  list && /^[[:space:]]*-/ {
    sub(/^[[:space:]]*-[[:space:]]*/, "")
    sub(/[[:space:]]+$/, "")
    print
    next
  }
  list { list = 0 }
' "$config" | sed '/^[[:space:]]*$/d' > "$work/allowed"
if [ ! -s "$work/allowed" ]; then
  echo "dependency-review: $config allows no licences" >&2
  exit 3
fi

# An SPDX expression such as "Apache-2.0 AND MIT" is only satisfied when every
# licence in it is allowed, so every identifier in it is checked. "AND", "OR"
# and "WITH" join the identifiers and are not licences themselves.
allowed_expression() {
  local expression="$1" identifier
  for identifier in $expression; do
    identifier="${identifier//(/}"
    identifier="${identifier//)/}"
    case "$identifier" in
      "" | AND | OR | WITH) continue ;;
    esac
    grep -qxF "$identifier" "$work/allowed" || return 1
  done
  return 0
}

# The Go modules a go.mod requires, as "path version" lines.
go_modules() {
  awk '
    /^require[ \t]*\(/ { block = 1; next }
    block && /^\)/ { block = 0; next }
    block { print; next }
    /^require[ \t]/ { print; next }
  ' | sed -e 's/^require[ \t]\{1,\}//' -e 's://.*::' -e 's/[[:space:]]\{1,\}/ /g' \
    -e 's/^ //' -e 's/ $//' | grep -v '^$'
}

# The dependencies a package.json names, as "name version" lines.
npm_dependencies() {
  node -e '
    const fs = require("fs");
    const text = fs.readFileSync(process.argv[1], "utf8");
    if (!text.trim()) process.exit(0);
    const pkg = JSON.parse(text);
    const deps = Object.assign({}, pkg.dependencies, pkg.devDependencies);
    for (const [name, version] of Object.entries(deps)) {
      console.log(name + " " + version);
    }
  ' "$1"
}

# The changes the pull request makes, as "kind manifest name version" lines.
: > "$work/changed"

git show "$base:go.mod" > "$work/base.go.mod" 2>/dev/null || : > "$work/base.go.mod"
git show "$head:go.mod" > "$work/head.go.mod" 2>/dev/null || : > "$work/head.go.mod"
go_modules < "$work/base.go.mod" | sort > "$work/base.modules"
go_modules < "$work/head.go.mod" | sort > "$work/head.modules"
# A line the head has and the base has not is a module the pull request adds,
# or one it bumps to another version: either way the licence may have moved.
comm -13 "$work/base.modules" "$work/head.modules" | while read -r path version; do
  echo "go go.mod $path $version" >> "$work/changed"
done

while read -r manifest; do
  git show "$base:$manifest" > "$work/base.json" 2>/dev/null || : > "$work/base.json"
  git show "$head:$manifest" > "$work/head.json" 2>/dev/null || : > "$work/head.json"
  npm_dependencies "$work/base.json" | sort > "$work/base.deps"
  npm_dependencies "$work/head.json" | sort > "$work/head.deps"
  comm -13 "$work/base.deps" "$work/head.deps" | while read -r name version; do
    echo "npm $manifest $name $version" >> "$work/changed"
  done
done < <(git ls-files '*package.json' | grep -v node_modules | sort -u)

if [ ! -s "$work/changed" ]; then
  echo "dependency-review: the pull request changes no dependency"
  exit 0
fi

# The licences of the Go modules, read from the LICENSE files in the module
# cache. The report is one line per package: "package,url,licence[,licence]".
# It is collected for Linux and for Windows, because a dependency can be
# linked into the program on one platform and not on the other (cobra pulls
# in mousetrap on Windows only), and a dependency that is not linked into
# either build is not in the report.
GOBIN="$work" go install github.com/google/go-licenses@v1.6.0 2> "$work/install.log" || {
  echo "dependency-review: could not install the licence reader" >&2
  sed -n '1,20p' "$work/install.log" >&2
  exit 3
}
: > "$work/report.csv"
for platform in linux windows; do
  if ! GOOS="$platform" "$work/go-licenses" report --include_tests ./... \
    >> "$work/report.csv" 2> "$work/report.log"; then
    echo "dependency-review: could not read the licences of the Go modules" >&2
    sed -n '1,20p' "$work/report.log" >&2
    exit 3
  fi
done
awk -F, '{ for (i = 3; i <= NF; i++) if ($i != "") print $1 "\t" $i }' \
  "$work/report.csv" | sort -u > "$work/package-licences"

violations=0
unreadable=0

while read -r kind manifest name version; do
  case "$kind" in
    go)
      # Every package of the module counts, because a module can carry a
      # licence per directory: klauspost/compress is Apache-2.0 at the root
      # and BSD-3-Clause in some of its subdirectories.
      licences=$(awk -F'\t' -v m="$name" \
        '$1 == m || index($1, m "/") == 1 { print $2 }' "$work/package-licences" |
        sort -u)
      ;;
    npm)
      licences=$(npm view "$name@$version" license 2>/dev/null | head -1)
      ;;
    *)
      echo "dependency-review: unknown dependency kind $kind" >&2
      exit 3
      ;;
  esac

  if [ -z "$licences" ]; then
    echo "::warning file=$manifest::dependency-review could not read the licence of $name $version"
    unreadable=$((unreadable + 1))
    continue
  fi

  while IFS= read -r licence; do
    [ -n "$licence" ] || continue
    if allowed_expression "$licence"; then
      echo "dependency-review: $manifest: $name $version - $licence"
    else
      echo "::error file=$manifest::dependency-review: $name $version is licensed under $licence, which the project does not accept"
      violations=$((violations + 1))
    fi
  done <<< "$licences"
done < "$work/changed"

if [ "$violations" -gt 0 ]; then
  echo "dependency-review: $violations dependency change(s) carry a licence the project does not accept"
  exit 1
fi
if [ "$unreadable" -gt 0 ]; then
  echo "dependency-review: $unreadable dependency change(s) could not be read; a maintainer should look at them"
fi
echo "dependency-review: every licence is one the project accepts"
