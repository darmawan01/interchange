#!/usr/bin/env bash
# buf generate, with the retry `ix` already does internally.
#
# Remote plugins are rate limited per IP, and shared CI runners exhaust that
# quota routinely. A rate limit is not a broken build, and a drift gate that
# goes red because a registry was busy is a gate people learn to ignore.
set -euo pipefail

attempts=5
wait=2

for i in $(seq 1 $attempts); do
  err=$(mktemp)
  if buf generate "$@" 2>"$err"; then
    cat "$err" >&2
    rm -f "$err"
    exit 0
  fi
  if ! grep -qE 'resource_exhausted|too many requests' "$err" || [ "$i" -eq "$attempts" ]; then
    cat "$err" >&2
    rm -f "$err"
    exit 1
  fi
  echo "  rate limited by the registry; retrying in ${wait}s ($i/$attempts)" >&2
  rm -f "$err"
  sleep "$wait"
  wait=$((wait * 2))
done
