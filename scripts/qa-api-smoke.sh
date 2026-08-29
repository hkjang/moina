#!/usr/bin/env bash
set -Eeuo pipefail

readonly base_url="${1:-http://127.0.0.1:8080}"
readonly username="${2:-e2e-admin}"
readonly password="${3:-e2e-password-12345}"
readonly expected_version="${4:-$(tr -d '[:space:]' < VERSION)}"
readonly temporary="$(mktemp -d)"
readonly cookie="${temporary}/session.cookie"
trap 'rm -R -- "${temporary}"' EXIT

for command_name in curl jq; do
  command -v "${command_name}" >/dev/null || { printf '오류: %s가 필요합니다.\n' "${command_name}" >&2; exit 3; }
done

curl --fail --silent --show-error "${base_url}/healthz" | jq -e '(.data.status // .status) == "ok" or (.data.status // .status) == "healthy"' >/dev/null
curl --fail --silent --show-error "${base_url}/readyz" | jq -e '(.data.status // .status) == "ready"' >/dev/null
curl --fail --silent --show-error "${base_url}/api/v1/version" \
  | jq -e --arg version "${expected_version}" '(.data.version // .version) == $version and (.data.name // .name) == "moina"' >/dev/null

login_body="$(jq -nc --arg username "${username}" --arg password "${password}" '{username:$username,password:$password}')"
curl --fail --silent --show-error -c "${cookie}" \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  --data "${login_body}" "${base_url}/api/v1/auth/login" \
  | jq -e '(.data.user.username // .data.username // .user.username // .username) | type == "string" and length > 0' >/dev/null

curl --fail --silent --show-error -b "${cookie}" "${base_url}/api/v1/auth/me" \
  | jq -e --arg username "${username}" '(.data.username // .data.user.username // .username // .user.username) == $username' >/dev/null
curl --fail --silent --show-error "${base_url}/login" | grep --fixed-strings '<div id="root"></div>' >/dev/null

printf 'API smoke 통과: health/readiness/version/login/session/web shell\n'
