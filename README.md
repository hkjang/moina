# MOINA

**MOINA — 사람·관심사·지식과 AI가 모이는 소셜 지식 네트워크**

MOINA는 짧은 생각인 **Moin**, 답글 **Echo**, 재공유 **Remoin**, 관심사 공간 **Moim**, 개인화 피드 **Flow**를 중심으로 한 한국어 우선 SNS입니다. Go 모듈러 모놀리스와 React/TypeScript 웹 앱을 하나의 컨테이너로 제공하며, 외부 PostgreSQL만 준비하면 폐쇄망에서도 운영할 수 있습니다.

현재 서비스 버전은 `v0.1.0`입니다. 로그인 화면과 프로필 컨텍스트 메뉴에서도 같은 버전을 확인할 수 있습니다.

## 주요 기능

- Moin 작성, Echo, Remoin, 반응, Pocket과 Link(팔로우)
- Following/For Me Flow, Topic과 Pulse, 통합 검색, 실시간 알림
- 개인 프로필·피드·글자 크기 설정과 URL 기반 메뉴 복원
- Keycloak 등 표준 OIDC Provider의 Authorization Code + PKCE 연동
- 관리자 UI에서 OIDC, AI, 승인 정책과 역할·권한을 설정하고 신고·제재를 운영
- 개인별 API/MCP 키 생성, 권한 변경, 즉시 회전·폐기·만료
- 선택형 팀장 검토·승인·반려 정책(꺼져 있으면 관련 절차와 메뉴 제외)
- OpenAI-compatible AI streaming과 최대 262,144 output token 정책
- REST API, MCP JSON-RPC, WebSocket 알림
- 한국어 기본 UI, 16px 이상 본문, 반응형 레이아웃과 전용 메뉴 스크롤바

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

Keycloak client ID/secret, AI endpoint/key/model/token 상한, 승인 정책, 역할과 권한 등 나머지는 bootstrap 후 **서비스 관리자 화면**에서 설정합니다. OIDC client secret과 AI API key 같은 민감한 자격 증명은 암호화하고, 일반·승인·권한 설정은 PostgreSQL에서 revision과 감사 이력을 함께 관리합니다.

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

Docker build는 frontend test/build와 backend test/vet/build를 함께 실행하고 다음 이미지를 만듭니다.

```text
moina:v0.1.0
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
dist/moina-v0.1.0.tar.gz
dist/moina-v0.1.0.tar.gz.sha256
```

`.sha256`은 로컬 반입 검증용입니다. GitHub Release에는 사용자 요구에 따라 서비스 이미지 `moina-v0.1.0.tar.gz` 하나만 올리고 SHA256 값은 릴리스 본문에 기록합니다.

## 폐쇄망 배포

PostgreSQL 서버는 이미지에 포함하지 않습니다. 기관 표준 PostgreSQL을 먼저 준비하고 migration 권한이 있는 전용 계정의 DSN을 사용하세요.

```bash
sha256sum moina-v0.1.0.tar.gz
gzip -dc moina-v0.1.0.tar.gz | docker image load
docker image inspect moina:v0.1.0
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
```

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

`VERSION`과 동일한 annotated tag를 `main`의 검증된 commit에 push하면 GitHub Actions가 다시 빌드·테스트하고 단일 오프라인 asset을 게시합니다.

```bash
git tag -a v0.1.0 -m "moina v0.1.0"
git push origin main
git push origin v0.1.0
```

고정 규칙:

```text
image: moina:v버전          예: moina:v0.1.0
file:  moina-v버전.tar.gz  예: moina-v0.1.0.tar.gz
```

## 라이선스

[MIT](LICENSE)
