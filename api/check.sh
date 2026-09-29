#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
apidiff="go run golang.org/x/exp/cmd/apidiff@v0.0.0-20260908205506-85c1c2202aba"
module=$(go list -m)
packages="$module:api/baseline $module/astra:api/astra.baseline"

if grep -Eq '^[[:space:]]*replace([[:space:]]|\()' go.mod; then
	echo "api: go.mod carries a replace directive; a published module cannot" >&2
	exit 1
fi

nonstd='{{if not .Standard}}{{.Module.Path}}{{end}}'
for dep in $(go list -deps -f "$nonstd" ./astra | sort -u | grep -v "^$module\$" || true); do
	if [ "$dep" != github.com/coder/websocket ]; then
		echo "api: new runtime dependency $dep; astra ships with github.com/coder/websocket only" >&2
		exit 1
	fi
done
if go list -deps -f "$nonstd" . | grep -v "^$module\$" | grep -q .; then
	echo "api: $module imports a dependency; the /api/v1 client is standard library only" >&2
	exit 1
fi

if [ "${1:-}" = "--update" ]; then
	for entry in $packages; do
		$apidiff -w "${entry#*:}" "${entry%%:*}"
	done
	echo "api: the baselines now describe $module as it is in this tree"
	exit 0
fi

status=0
for entry in $packages; do
	report=$($apidiff -incompatible "${entry#*:}" "${entry%%:*}")
	if [ -n "$report" ]; then
		echo "package ${entry%%:*}"
		echo "$report"
		status=1
	fi
done
if [ "$status" -ne 0 ]; then
	echo "api: incompatible changes to the public Go API; they wait for a new major version" >&2
	exit 1
fi
echo "api: the public Go API is compatible with its baselines"
