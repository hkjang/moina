# MOINA

**MOINA — 사람·관심사·지식과 AI가 모이는 소셜 지식 네트워크**

MOINA는 짧은 생각인 **Moin**, 답글 **Echo**, 재공유 **Remoin**, 관심사 공간 **Moim**, 개인화 피드 **Flow**를 중심으로 한 한국어 우선 SNS입니다. Go 모듈러 모놀리스와 React/TypeScript 웹 앱을 하나의 컨테이너로 제공하며, 외부 PostgreSQL만 준비하면 폐쇄망에서도 운영할 수 있습니다.

현재 서비스 버전은 `v0.1.12`입니다. 로그인 화면과 프로필 컨텍스트 메뉴에서도 같은 버전을 확인할 수 있습니다.

`v0.1.12`는 `Ctrl/⌘+K` 전역 빠른 이동 팔레트, 키보드 결과 탐색, 최근 방문 복귀와 `G` 연속 화면 단축키를 제공합니다. 화면·설정·관리 메뉴는 현재 권한에 맞게 노출되고, 입력한 문장은 통합 검색으로 바로 이어집니다. `C`를 누르면 입력 중이거나 다른 Dialog를 사용 중이지 않을 때 새 Moin 작성을 즉시 시작합니다.

## 주요 기능

- 캡처 이미지 붙여넣기·드래그 앤 드롭·첨부 교체, `@사용자아이디` 자동완성·링크와 작성자 삭제를 지원하는 Moin 작성·수정, Echo, Remoin, 반응, Pocket과 Link(팔로우)
- 가입 회원의 전용 Moin·Echo·인용·Remoin을 같은 멤버 공개 범위로 유지하는 Moim 커뮤니티 대화
- `Ctrl/⌘+K`, 최근 방문, 권한 필터, 통합 검색과 `G` 연속 단축키를 결합한 전역 빠른 이동
- Following/For Me Flow, Topic과 Pulse, 통합 검색, 실시간 알림
- 최근 7일 공개 Moin·Signal과 24시간 가중치를 반영한 Topic Pulse
- 개인 프로필·피드·글자 크기 설정과 URL 기반 메뉴 복원
- Keycloak 등 표준 OIDC Provider의 Authorization Code + PKCE 연동
- 관리자 UI에서 OIDC, AI, SMTP 메일, 승인 정책과 역할·권한을 설정하고 신고·제재를 운영
- 개인별 API/MCP 키 생성, 권한 변경, 즉시 회전·폐기·만료
- 선택형 팀장 검토·승인·반려 정책(꺼져 있으면 관련 절차와 메뉴 제외)
- OpenAI-compatible AI streaming과 최대 262,144 output token 정책
- REST API, MCP JSON-RPC, WebSocket 알림
- 한국어 기본 UI, 16px 이상 본문, 반응형 레이아웃과 전용 메뉴 스크롤바
- 최근 후보 200개, 동결 점수·설정과 사용자당 활성 3개를 적용한 최대 1시간 For Me ranking snapshot
- `pg_trgm`·PostgreSQL 전문 검색을 결합한 한국어 부분·관련도 검색
- Transactional Outbox, 지수 백오프·Dead Letter와 관리자 재처리
- Prometheus `/metrics`, 요청 ID·구조화 로그, DB pool·Flow SQL 수·Outbox·WebSocket 관측
- OIDC·AI 목적지 exact-authority allowlist, 명시적 사설 hostname 예외와 DNS/IP·redirect 재검증
- PostgreSQL Large Object 스트리밍 미디어, 미첨부 100개·512 MiB quota와 인스턴스당 시간당 최대 10,000개 orphan drain
- 페이지별 Flow cache·ID 병합, optimistic update와 route lazy loading
- 알림 LISTEN backpressure, slow WebSocket 재연결과 60초 REST 정합성 보완
- In App·Toast·Desktop·SMTP Email·Digest·조용한 시간 알림 개인화와 필수 승인 알림 정책
- 신뢰 Proxy IP/CIDR 기반 실제 Client IP 계산, 감사 Proxy Chain과 다중 인스턴스 공용 PostgreSQL 요청 한도
- TLS Proxy bootstrap용 Fetch Metadata Origin 검증, immutable hash asset과 stale chunk 404
- Moin 관계별 대체 텍스트와 엄격한 승인 Action 패턴·승인자 최종 권한 검증

## 기술 구성

| 계층 | 구성 |
| --- | --- |
| Frontend | React, TypeScript, Vite, Tailwind CSS, Radix UI |
| Backend | Go, REST, SSE, WebSocket, MCP |
| Database | 외부 PostgreSQL |
| Authentication | 로컬 세션, Keycloak/OIDC |
| Runtime | distroless, non-root, read-only Docker container |

브라우저 런타임에서 외부 CDN, 웹 폰트, 분석 스크립트를 사용하지 않습니다. 빌드에 필요한 모듈은 연결된 빌드 구역에서 내려받지만 완성된 서비스 이미지는 registry나 package repository에 접근하지 않습니다. OIDC·AI 같은 선택 기능은 폐쇄망 안에서 도달 가능한 내부 endpoint를 사용해야 합니다.

## 런타임 계약

서비스가 읽는 환경변수는 정확히 네 개입니다.

| 환경변수 | 설명 |
| --- | --- |
| `MOINA_POSTGRES_DSN` | 외부 PostgreSQL DSN |
| `MOINA_BOOTSTRAP_ADMIN` | 최초 로컬 최고 관리자 아이디 |
| `MOINA_BOOTSTRAP_ADMIN_PASSWORD` | 최초 관리자 비밀번호 |
| `MOINA_ENCRYPTION_KEY` | 저장 비밀값을 보호하는 32바이트 root key |

Keycloak client ID/secret, AI endpoint/key/model/token 상한, SMTP endpoint/password, 승인 정책, 역할과 권한 등 나머지는 bootstrap 후 **서비스 관리자 화면**에서 설정합니다. OIDC client secret, AI API key와 SMTP password 같은 민감한 자격 증명은 암호화하고, 일반·승인·권한 설정은 PostgreSQL에서 revision과 감사 이력을 함께 관리합니다.

```bash
cp .env.example .env
# .env의 네 값을 운영 비밀값으로 교체
```

`MOINA_ENCRYPTION_KEY`는 다음처럼 만들 수 있습니다.

```bash
openssl rand -base64 32
```

## 개발과 검증

Go 1.26.6 이상, Node.js 24, npm, Docker 24+와 Docker Compose v2가 필요합니다.

```bash
make check
make test
make image
```

로고 SVG를 수정한 뒤 PWA PNG 아이콘과 OG WebP를 재현하려면 Playwright Chromium과 FFmpeg가 준비된 연결 빌드 구역에서 `make brand-assets`를 실행합니다. 생성 결과의 고정 SHA-256과 이전 Purple·Teal 색상 잔존 여부는 `make check`가 검증합니다.

Docker build는 frontend test/build와 backend test/vet/build를 함께 실행하고 다음 이미지를 만듭니다.

```text
moina:v0.1.12
```

브라우저 E2E는 임시 PostgreSQL과 테스트 전용 계정으로 실행합니다. 자세한 명령은 [E2E 안내](e2e/README.md)를 참고하세요.

## 오프라인 이미지 패키지

인터넷에 연결된 검증 빌드 구역에서 실행합니다.

```bash
make image
make package
make verify-package
```

산출물은 다음과 같습니다.

```text
dist/moina-v0.1.12.tar.gz
dist/moina-v0.1.12.tar.gz.sha256
```

`.sha256`은 로컬 반입 검증용입니다. GitHub Release에는 사용자 요구에 따라 서비스 이미지 `moina-v0.1.12.tar.gz` 하나만 올리고 SHA256 값은 릴리스 본문에 기록합니다.

## 폐쇄망 배포

PostgreSQL 서버는 이미지에 포함하지 않습니다. 기관 표준 PostgreSQL을 먼저 준비하고 migration 권한이 있는 전용 계정의 DSN을 사용하세요.

```bash
sha256sum moina-v0.1.12.tar.gz
gzip -dc moina-v0.1.12.tar.gz | docker image load
docker image inspect moina:v0.1.12
docker compose --env-file .env \
  -f deploy/docker-compose.offline.yml \
  up -d --pull never
```

기본 compose는 `127.0.0.1:8080`에만 bind합니다. 운영에서는 같은 호스트의 기관 표준 TLS reverse proxy 뒤에 배치하세요. 사설 CA가 필요하면 추가 환경변수 없이 기관 CA bundle을 mount할 수 있습니다.

```bash
docker compose --env-file .env \
  -f deploy/docker-compose.offline.yml \
  -f deploy/docker-compose.private-ca.yml \
  up -d --pull never
```

상태 확인:

```bash
curl --fail http://127.0.0.1:8080/healthz
curl --fail http://127.0.0.1:8080/readyz
curl --fail http://127.0.0.1:8080/api/v1/version
curl --fail http://127.0.0.1:8080/metrics
```

시작 migration은 대규모 검색 index 생성을 고려해 최대 30분까지 실행되며 완료 전에는 readiness가 성공하지 않습니다. DB에 현재 binary가 모르는 migration version이 있으면 안전하지 않은 downgrade로 판단해 기동을 거부합니다.

### v0.1.0에서 업그레이드할 때

사설 Keycloak/OIDC 또는 AI endpoint를 사용했다면 기존 host를 자동으로 사설망 허용 대상으로 승격하지 않습니다. 업그레이드 전에 로컬 bootstrap 최고 관리자 로그인을 확인하고, 업그레이드 후 그 계정으로 각 endpoint의 정확한 DNS hostname을 `allowedHosts`와 `privateAllowedHosts`에 명시 저장한 뒤 연결 테스트를 수행해야 합니다. 이미 생성된 bootstrap 계정의 비밀번호는 환경변수 변경만으로 재설정되지 않습니다. 자세한 절차는 [오프라인 운영 가이드](docs/operations.md#v010-내부-oidcai-사용자의-필수-조치)를 따르세요.

## 문서

- [제품 소개](https://hkjang.github.io/moina/)
- [사용자 가이드](https://hkjang.github.io/moina/user-guide.html)
- [관리자 가이드](https://hkjang.github.io/moina/admin-guide.html)
- [설정](docs/configuration.md)
- [오프라인 운영](docs/operations.md)
- [보안과 키 관리](docs/security.md)
- [REST API와 MCP](docs/api-mcp.md)
- [아키텍처](docs/architecture.md)
- [OpenAPI 3.1](api/openapi.yaml)

## 릴리스

`VERSION`과 동일한 annotated tag를 `main`의 검증된 commit에 push하면 GitHub Actions가 다시 빌드·테스트하고 단일 오프라인 asset을 게시합니다. 먼저 `main`을 push하고 그 정확한 commit의 `CI` 성공을 확인한 뒤 tag를 push해야 합니다.

```bash
git push origin main
# GitHub Actions의 해당 commit CI 성공 확인
git tag -a v0.1.12 -m "moina v0.1.12"
git push origin v0.1.12
```

고정 규칙:

```text
image: moina:v버전          예: moina:v0.1.12
file:  moina-v버전.tar.gz  예: moina-v0.1.12.tar.gz
```

## 라이선스

[MIT](LICENSE)
