#!/usr/bin/env bash
set -Eeuo pipefail

readonly app_name="moina"
readonly version="${1:-$(tr -d '[:space:]' < VERSION)}"
readonly output_dir="${2:-dist}"

if [[ ! "${version}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
  printf '오류: 버전은 vX.Y.Z 형식이어야 합니다: %s\n' "${version}" >&2
  exit 2
fi
command -v docker >/dev/null || { printf '오류: docker가 필요합니다.\n' >&2; exit 3; }
command -v sha256sum >/dev/null || { printf '오류: sha256sum이 필요합니다.\n' >&2; exit 3; }

readonly image_ref="${app_name}:${version}"
readonly archive_name="${app_name}-${version}.tar.gz"
readonly checksum_name="${archive_name}.sha256"
docker image inspect "${image_ref}" >/dev/null 2>&1 || { printf '오류: 로컬 이미지가 없습니다: %s\n' "${image_ref}" >&2; exit 4; }

readonly platform="$(docker image inspect --format '{{.Os}}/{{.Architecture}}' "${image_ref}")"
if [[ "${platform}" != "linux/amd64" ]]; then
  printf '오류: linux/amd64 이미지가 필요합니다: %s\n' "${platform}" >&2
  exit 4
fi

mkdir -p "${output_dir}"
readonly output_abs="$(cd "${output_dir}" && pwd -P)"
temporary="$(mktemp "${output_abs}/.${archive_name}.XXXXXX")"
readonly temporary
trap 'rm -f -- "${temporary}"' EXIT

docker image save "${image_ref}" | gzip -n -9 > "${temporary}"
gzip -t "${temporary}"
mv -f -- "${temporary}" "${output_abs}/${archive_name}"
(
  cd "${output_abs}"
  sha256sum "${archive_name}" > "${checksum_name}"
)
trap - EXIT
printf '완료: %s\n' "${output_abs}/${archive_name}"
printf 'SHA256: '
cut -d ' ' -f 1 "${output_abs}/${checksum_name}"
