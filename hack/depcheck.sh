#!/usr/bin/env bash
# The dependency rule, enforced rather than documented:
#
#   Core depends on interfaces. Adapters depend on core. Nothing depends on a
#   concrete adapter.
#
# This asserts the core module's package graph contains no broker client, no
# HTTP router, no policy engine and no auth module. It is an allowlist rather
# than a denylist: a denylist only catches the seams that have already leaked.
#
# Retrofitting this check later is painful. It costs nothing now.
set -euo pipefail

cd "$(dirname "$0")/.."

MODULE=github.com/darmawan01/interchange

# Everything core is allowed to import, and why.
ALLOWED=(
  "google.golang.org/protobuf"  # the IR and the codecs
  "connectrpc.com/connect"      # the request/response binding's protocol
)

fail=0
while read -r pkg; do
  [[ -z "$pkg" ]] && continue
  # Standard library packages have no dot in their first path element.
  first=${pkg%%/*}
  [[ "$first" != *.* ]] && continue
  # Core's own packages.
  [[ "$pkg" == "$MODULE" || "$pkg" == "$MODULE"/* ]] && continue

  ok=0
  for allow in "${ALLOWED[@]}"; do
    if [[ "$pkg" == "$allow" || "$pkg" == "$allow"/* ]]; then ok=1; break; fi
  done
  if [[ $ok -eq 0 ]]; then
    echo "  ✗ core imports $pkg"
    fail=1
  fi
done < <(go list -deps ./... 2>/dev/null)

if [[ $fail -ne 0 ]]; then
  cat <<'MSG'

The core module has grown a dependency outside its allowlist. A seam has
leaked: core must not import a broker client, an HTTP router, a policy engine
or the auth module.

If the dependency is genuinely core's business, add it to ALLOWED in
hack/depcheck.sh together with the reason it belongs there.
MSG
  exit 1
fi

# The same rule stated as the thing a reviewer actually greps for.
if go list -deps ./... | grep -Eq 'nats-io|eclipse/paho|gorilla|coder/websocket|open-policy-agent|casbin|/auth$'; then
  echo "✗ core's graph contains a broker, a router, or a policy engine"
  exit 1
fi

echo "✓ core's dependency graph is clean (protobuf + connect only)"
