#!/usr/bin/env bash
# Print the next YYYY.M.PATCH version: today's year and month, with the patch
# number one past the highest tag already published this month.
set -euo pipefail
TODAY="$(date +%Y.%-m)"
LATEST="$(git tag --list "${TODAY}.*" 2>/dev/null | sort -V | tail -1)"
if [ -z "$LATEST" ]; then
  echo "${TODAY}.0"
else
  PATCH="${LATEST##*.}"
  echo "${TODAY}.$((PATCH + 1))"
fi
