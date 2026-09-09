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
trap 'rm -f coverage.txt' EXIT

readonly log_file="${LOG_FILE:-/tmp/$(basename "$0" .sh).log}"
exec > >(tee -a "$log_file") 2>&1

readonly min_test_coverage="${MIN_TEST_COVERAGE:-90}"
if ! [[ "$min_test_coverage" =~ ^[0-9]+$ ]]; then
	log ERROR "MIN_TEST_COVERAGE must be a whole number"
	exit 2
fi

mapfile -t coverage_packages < <(go list ./... | grep -v '/elelemtest/mocks')
if ((${#coverage_packages[@]} == 0)); then
	log ERROR "no packages eligible for coverage"
	exit 1
fi

coverpkg="$(
	IFS=,
	printf '%s' "${coverage_packages[*]}"
)"
log INFO "running race-enabled coverage suite"
go test -count=1 -race -coverpkg="$coverpkg" -coverprofile=coverage.txt "${coverage_packages[@]}"

pct="$(go tool cover -func=coverage.txt | awk '/^total:/ { gsub(/%/, "", $3); print $3 }')"
: "${pct:=0}"
printf '%s\n' "$pct" >coverage-percent.txt

result="${pct%.*}"
: "${result:=0}"

if ((result == 0)); then
	log WARN "no test coverage information available"
	exit 0
fi

if ((result < min_test_coverage)); then
	log ERROR "coverage ${pct}% is below the required ${min_test_coverage}%"
	exit 1
fi

if [[ -n "${DEBUG:-}" ]]; then
	log DEBUG "coverage artifact written to coverage-percent.txt"
fi
log INFO "coverage ${pct}% meets the required ${min_test_coverage}%"
