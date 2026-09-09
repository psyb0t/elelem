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

readonly log_file="${LOG_FILE:-/tmp/$(basename "$0" .sh).log}"
exec > >(tee -a "$log_file") 2>&1

readonly sarif_out="${SARIF_OUT:-sec.sarif}"
tmp_dir="$(mktemp -d)"
readonly tmp_dir
trap 'rm -rf "$tmp_dir"' EXIT

log INFO "running govulncheck SARIF report"
govulncheck -format sarif ./... >"$tmp_dir/govulncheck.sarif"

govulncheck_status=0
log INFO "running govulncheck gate"
govulncheck ./... || govulncheck_status=$?

semgrep_status=0
log INFO "running semgrep"
semgrep scan \
	--config=p/golang \
	--config=p/security-audit \
	--exclude=vendor \
	--sarif \
	--output="$tmp_dir/semgrep.sarif" \
	--metrics=off \
	--error \
	. || semgrep_status=$?

log INFO "merging SARIF reports"
jq -s \
	'{version: "2.1.0", "$schema": "https://json.schemastore.org/sarif-2.1.0.json", runs: ((.[0].runs // []) + (.[1].runs // []))}' \
	"$tmp_dir/govulncheck.sarif" "$tmp_dir/semgrep.sarif" >"$sarif_out"

if ((govulncheck_status != 0 || semgrep_status != 0)); then
	log ERROR "security scan found issues govulncheck=${govulncheck_status} semgrep=${semgrep_status}"
	exit 1
fi

if [[ -n "${DEBUG:-}" ]]; then
	log DEBUG "SARIF report written to ${sarif_out}"
fi
log INFO "security scan found no issues"
