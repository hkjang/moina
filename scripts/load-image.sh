#!/usr/bin/env bash
set -Eeuo pipefail

readonly version="${1:-$(tr -d '[:space:]' < VERSION)}"
readonly archive="${2:-moina-${version}.tar.gz}"
readonly image="moina:${version}"

bash "$(dirname "$0")/verify-image-package.sh" "${archive}" "${image}"
docker image inspect "${image}" >/dev/null 2>&1 || { printf '오류: 로드 후 이미지가 없습니다: %s\n' "${image}" >&2; exit 4; }
printf '이미지 준비 완료: %s\n' "${image}"
