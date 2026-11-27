#!/bin/bash
set -euo pipefail

log() {
	local level="$1"
	shift

	jq -cn \
		--arg time "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" \
		--arg level "$level" \
		--arg file "${BASH_SOURCE[1]##*/}" \
		--argjson line "${BASH_LINENO[0]}" \
		--arg func "${FUNCNAME[1]:-main}" \
		--arg msg "$*" \
		'{time: $time, level: $level, file: $file, line: $line, func: $func, msg: $msg}' >&2
}

on_error() {
	local exit_code="$?"

	log ERROR "command failed exit=${exit_code}"
	exit "$exit_code"
}

trap on_error ERR

readonly LOG_FILE="${LOG_FILE:-/tmp/$(basename "$0" .sh).log}"
exec > >(tee -a "$LOG_FILE") 2>&1

# A third-party release must be this old before it is taken. Modules under
# FIRST_PARTY are published by this project's owner and skip the wait.
readonly SECONDS_PER_DAY=86400
readonly MIN_AGE_DAYS=7
readonly FIRST_PARTY="github.com/psyb0t/"
# A release, or a pseudo-version for a module that publishes no releases.
readonly MODULE_PATTERN='^[a-z0-9.-]+(/[A-Za-z0-9._~-]+)+@v[0-9]+\.[0-9]+\.[0-9]+(-[0-9]{14}-[0-9a-f]{12})?$'

read -r -a packages <<<"${PKG:-}"
if ((${#packages[@]} == 0)); then
	log ERROR "set PKG to one or more module@vX.Y.Z values"
	exit 2
fi

now="$(date -u +%s)"
readonly now

# Every candidate is checked before go.mod changes, so a refused one leaves
# the module graph as it was. go list asks the module proxy and fails when
# the version does not exist.
for package in "${packages[@]}"; do
	if ! [[ "$package" =~ $MODULE_PATTERN ]]; then
		log ERROR "not an exact module@vX.Y.Z release: ${package}"
		exit 2
	fi

	module="${package%@*}"
	version="${package##*@}"

	published="$(go list -mod=mod -m -json "$package" | jq -er --arg version "$version" \
		'select(.Version == $version) | .Time | sub("\\.[0-9]+"; "") | fromdateiso8601')"
	age_days=$(((now - published) / SECONDS_PER_DAY))

	if ((now < published)); then
		log ERROR "${package} is dated in the future"
		exit 1
	fi

	if [[ "$module" != "$FIRST_PARTY"* ]] && ((age_days < MIN_AGE_DAYS)); then
		log ERROR "${package} is ${age_days} days old, under the ${MIN_AGE_DAYS}-day minimum"
		exit 1
	fi

	log INFO "accepted ${package} age_days=${age_days}"
done

go get "${packages[@]}"
go mod tidy
go mod vendor
log INFO "updated ${#packages[@]} modules"
