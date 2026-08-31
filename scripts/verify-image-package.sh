#!/usr/bin/env bash
set -Eeuo pipefail

readonly archive="${1:-dist/moina-v0.1.5.tar.gz}"
readonly expected_image="${2:-moina:v0.1.5}"
readonly checksum_file="${archive}.sha256"
readonly expected_name="${expected_image%%:*}-${expected_image#*:}.tar.gz"

for command_name in docker gzip tar jq sha256sum; do
  command -v "${command_name}" >/dev/null || { printf '오류: %s가 필요합니다.\n' "${command_name}" >&2; exit 3; }
done
[[ "$(basename "${archive}")" == "${expected_name}" ]] || { printf '오류: 파일명 규칙 위반(예상 %s).\n' "${expected_name}" >&2; exit 2; }
[[ -f "${archive}" ]] || { printf '오류: 패키지가 없습니다: %s\n' "${archive}" >&2; exit 2; }
readonly archive_dir="$(cd "$(dirname "${archive}")" && pwd -P)"
if [[ -f "${checksum_file}" ]]; then
  (
    cd "${archive_dir}"
    sha256sum --check "$(basename "${checksum_file}")"
  )
else
  printf '참고: 별도 checksum 파일이 없습니다. 릴리스 노트와 대조할 SHA256: '
  sha256sum "${archive}" | cut -d ' ' -f 1
fi
gzip -t "${archive}"

readonly manifest="$(gzip -dc "${archive}" | tar -xOf - manifest.json)"
jq -e --arg image "${expected_image}" '
  length == 1 and .[0].RepoTags == [$image] and
  (.[0].Config | type == "string" and (
    test("^blobs/sha256/[0-9a-f]{64}$") or
    test("^[0-9a-f]{64}\\.json$")
  ))
' <<<"${manifest}" >/dev/null || { printf '오류: archive에는 정확히 %s 이미지 하나만 있어야 합니다.\n' "${expected_image}" >&2; exit 4; }

readonly config_path="$(jq -er '.[0].Config' <<<"${manifest}")"
[[ "${config_path}" =~ ^blobs/sha256/[0-9a-f]{64}$ || "${config_path}" =~ ^[0-9a-f]{64}\.json$ ]] || { printf '오류: 잘못된 image config 경로입니다.\n' >&2; exit 4; }
readonly config="$(gzip -dc "${archive}" | tar -xOf - "${config_path}")"
readonly expected_version="${expected_image#*:}"
jq -e --arg version "${expected_version}" --arg source "https://github.com/hkjang/moina" '
  .os == "linux" and .architecture == "amd64" and
  .config.User == "nonroot:nonroot" and
  .config.Entrypoint == ["/app/moina"] and
  .config.Healthcheck.Test == ["CMD", "/app/moina", "healthcheck"] and
  .config.Labels["org.opencontainers.image.title"] == "moina" and
  .config.Labels["org.opencontainers.image.version"] == $version and
  .config.Labels["org.opencontainers.image.source"] == $source and
  (.config.Labels["org.opencontainers.image.revision"] | test("^(unknown|[0-9a-f]{40})$"))
' <<<"${config}" >/dev/null || { printf '오류: 플랫폼·nonroot·OCI label 검증 실패.\n' >&2; exit 4; }

# Parse every layer through Docker's importer, then save the loaded tag again and
# compare the image config content address. Docker's classic and containerd image
# stores expose different values through `.Id`, while both save formats retain
# the same content-addressed config referenced by manifest.json.
gzip -dc "${archive}" | docker image load >/dev/null
readonly config_digest="${config_path#blobs/sha256/}"
readonly normalized_digest="${config_digest%.json}"
[[ "${normalized_digest}" =~ ^[0-9a-f]{64}$ ]] || { printf '오류: archive config digest가 올바르지 않습니다.\n' >&2; exit 4; }
readonly loaded_manifest="$(docker image save "${expected_image}" | tar -xOf - manifest.json)"
jq -e --arg image "${expected_image}" '
  length == 1 and .[0].RepoTags == [$image] and
  (.[0].Config | type == "string" and (
    test("^blobs/sha256/[0-9a-f]{64}$") or
    test("^[0-9a-f]{64}\\.json$")
  ))
' <<<"${loaded_manifest}" >/dev/null || { printf '오류: docker load 후 태그 또는 manifest가 예상과 다릅니다.\n' >&2; exit 4; }
readonly loaded_config_path="$(jq -er '.[0].Config' <<<"${loaded_manifest}")"
readonly loaded_config_digest="${loaded_config_path#blobs/sha256/}"
readonly loaded_normalized_digest="${loaded_config_digest%.json}"
[[ "${loaded_normalized_digest}" == "${normalized_digest}" ]] || { printf '오류: docker load 후 image config content-address가 archive와 다릅니다.\n' >&2; exit 4; }

printf '패키지 검증 완료: %s (%s, linux/amd64)\n' "${archive}" "${expected_image}"
