#!/usr/bin/env bash
set -Eeuo pipefail

readonly expected=$'MOINA_BOOTSTRAP_ADMIN\nMOINA_BOOTSTRAP_ADMIN_PASSWORD\nMOINA_ENCRYPTION_KEY\nMOINA_POSTGRES_DSN'

mapfile -t files < <(find backend -type f -name '*.go' ! -name '*_test.go' -print 2>/dev/null | sort)
if [[ "${#files[@]}" -eq 0 ]]; then
  printf '오류: 검사할 Go 런타임 소스가 없습니다.\n' >&2
  exit 2
fi

actual="$(grep -hEo 'MOINA_[A-Z0-9_]+' "${files[@]}" | sort -u || true)"
if [[ "${actual}" != "${expected}" ]]; then
  printf '오류: 런타임 환경변수 계약은 정확히 네 개여야 합니다.\n' >&2
  printf '예상:\n%s\n실제:\n%s\n' "${expected}" "${actual:-없음}" >&2
  exit 1
fi

compose_actual="$(grep -Eo 'MOINA_[A-Z0-9_]+' deploy/docker-compose.offline.yml | sort -u)"
if [[ "${compose_actual}" != "${expected}" ]]; then
  printf '오류: Compose 환경변수 계약이 서버와 다릅니다.\n' >&2
  exit 1
fi

printf '런타임 환경변수 계약 통과: 4개\n'
