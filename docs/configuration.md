# MOINA 설정 가이드

이 문서는 `v0.1.6`의 환경변수 계약과 관리자 화면에서 변경할 수 있는 설정을 구분합니다.

## 런타임 환경변수

MOINA 프로세스가 읽는 환경변수는 정확히 네 개입니다.

| 이름 | 필수 | 형식 | 목적 |
| --- | --- | --- | --- |
| `MOINA_POSTGRES_DSN` | 예 | PostgreSQL URI | 외부 PostgreSQL 연결 |
| `MOINA_BOOTSTRAP_ADMIN` | 예 | 줄바꿈 없는 UTF-8 1~120자 아이디 | 최초 로컬 최고 관리자 |
| `MOINA_BOOTSTRAP_ADMIN_PASSWORD` | 예 | 12자 이상, UTF-8 72바이트 이하 | 최초 관리자 비밀번호 |
| `MOINA_ENCRYPTION_KEY` | 예 | base64/hex로 표현한 32바이트 | 저장 비밀값 envelope 암호화 root |

예시:

```dotenv
MOINA_POSTGRES_DSN=postgres://moina:secret@postgres.internal:5432/moina?sslmode=verify-full
MOINA_BOOTSTRAP_ADMIN=platform-admin
MOINA_BOOTSTRAP_ADMIN_PASSWORD=replace-with-vault-secret
MOINA_ENCRYPTION_KEY=replace-with-base64-32-byte-value
```

`.env`는 `0600` 권한으로 관리하고 Git에 커밋하지 않습니다. DSN에 특수 문자가 있으면 URI encoding을 적용합니다.

## 관리자 화면 설정

### 일반

- 서비스 표시 이름, 외부 사이트 기본 주소(`publicBaseUrl`), 기본 시간대와 가입 허용 여부
- session 유지 시간(5~1,440분)
- 개인 API key 인증과 MCP 활성화 여부, key별 분당 요청 한도
- 업로드 파일 한도(64 KiB~50 MiB), Moin당 미디어 수(1~12)와 미사용 업로드 보존 시간(1~720시간, 기본 24시간)
- 신뢰할 Reverse Proxy의 정확한 IP 또는 CIDR 목록(최대 128개)

새 미디어는 PostgreSQL Large Object로 일정한 메모리 사용량 안에서 업로드·다운로드되며, Moin이나 프로필에 연결되지 않은 업로드는 설정된 TTL이 지나면 내장 정리 작업이 삭제합니다. 기존 `bytea` 미디어는 호환 읽기를 유지합니다. 사용자별 미첨부 quota 100개·512 MiB, 인스턴스당 동시 Large Object read 최대 8개와 cleaner 한 주기 최대 10,000개는 `v0.1.6`의 고정 안전 한도이며 관리자 설정으로 변경할 수 없습니다. DB pool이 작으면 media read보다 일반 API 연결 5개를 우선 남깁니다.

업로드 시 입력한 대체 텍스트는 재사용 기본값입니다. Moin 작성·수정 시 최종 대체 텍스트를 `post_media` 관계에 따로 저장하므로 같은 media ID를 여러 Moin에서 사용해도 각 문맥의 설명이 서로 덮어쓰이지 않습니다.

작성 화면은 인증 후 `GET /api/v1/media/config`를 조회해 현재 `maxUploadBytes`, `maxPerPost`, `acceptedTypes`를 적용합니다. 이 응답에는 관리자 전용 보존 값인 `orphanTtlHours`가 포함되지 않습니다. 설정을 바꾼 뒤 이미 작성 화면을 열어 둔 사용자는 업로드 전에 이 계약을 다시 조회해야 하며, 서버 검증이 최종 기준입니다.

`publicBaseUrl`은 path/query/fragment가 없는 외부 HTTP(S) origin입니다. OIDC 전용 `redirectUrl`이 비어 있을 때 `${publicBaseUrl}/api/v1/auth/oidc/callback`을 사용하며, 둘 다 비어 있을 때만 신뢰된 현재 요청 주소로 계산합니다. 동시 session 수, Moin 글자 수, WebSocket 및 검색 정책은 `v0.1.6` 관리자 설정 모델에 포함되지 않습니다.

### Reverse Proxy와 요청 한도

`network.proxy.trustedProxies`에는 MOINA에 직접 연결하는 Proxy Peer의 IP 또는 CIDR만 입력합니다. 직접 연결 Peer가 목록에 있을 때만 표준 `Forwarded` 또는 `X-Forwarded-For`·`X-Forwarded-Proto`를 신뢰하며, 오른쪽부터 신뢰 Proxy를 제거해 실제 Client IP를 계산합니다. 전달 protocol은 가장 가까운 오른쪽 hop의 값만 사용해 왼쪽의 사용자 입력이 secure cookie 판단을 바꾸지 못하게 합니다. 빈 목록은 모든 전달 헤더를 무시하는 안전한 기본값입니다. 감사 기록에는 `socketIp`, `clientIp`, `proxyChain`을 분리해 남깁니다.

로그인은 사용자명+Client IP 5분당 5회와 사용자명 전체 5분당 20회를 함께 제한하고, 가입은 Client IP 기준 10분당 5회입니다. 개인 API·MCP key의 `rateLimitPerMinute`도 PostgreSQL `rate_limit_buckets`를 사용하므로 여러 MOINA 인스턴스가 같은 quota를 공유합니다. Rate Limit 저장소를 사용할 수 없으면 인증·키 요청을 허용하지 않고 `rate_limit_unavailable`을 반환합니다.

### Keycloak/OIDC

- 활성화 여부
- issuer URL
- client ID와 client secret
- redirect URL
- scopes, claim mapping과 기본 역할
- 자동 사용자 생성 여부
- outbound 전체 허용 호스트(`allowedHosts`), 사설 주소 예외 hostname(`privateAllowedHosts`)과 폐쇄망 HTTP 허용 여부

Client secret은 암호화해 저장하고 API 응답에 반환하지 않습니다. 빈 값은 기존 secret 유지, 명시적 삭제 동작은 제거로 구분합니다.

`allowedHosts`에는 DNS 이름 또는 IP를 정확히 입력합니다. port 없는 항목은 URL scheme의 기본 port(HTTPS 443, HTTP 80)에만 일치하므로 8443 같은 비기본 port는 `host:port`로 등록해야 합니다. scheme, 경로, 사용자 정보와 wildcard는 허용하지 않습니다. 기존 설정에서 목록이 비어 있으면 현재 issuer의 정확한 host와 비기본 port만 초기값으로 채웁니다.

DNS가 RFC1918 IPv4 또는 ULA IPv6를 반환하는 폐쇄망 endpoint는 정확한 DNS hostname을 `privateAllowedHosts`에도 등록합니다. 같은 authority가 `allowedHosts`에 먼저 있어야 하며, 비기본 port라면 두 목록 모두 같은 `host:port`를 사용합니다. IP literal은 사설 주소 예외 목록에 넣지 않습니다. loopback, link-local, cloud metadata, CGNAT, unspecified와 multicast 주소는 `privateAllowedHosts`로도 열 수 없습니다. 저장·연결 테스트와 실제 로그인 중 최초 URL, discovery가 제공한 endpoint, redirect, DNS 해석 결과를 매 단계 다시 검증합니다.

관리자 연결 테스트는 사설망 정책 차단(`oidc_private_host_denied`), 허용 host 누락, DNS, TLS 인증서, timeout과 discovery issuer 불일치를 서로 다른 오류로 안내합니다. Issuer URL은 discovery 문서의 `issuer`와 정확히 일치해야 하며 공급자가 trailing slash를 사용하는 경우 그대로 입력합니다. 내부 CA는 전체 PEM bundle을 표준 trust 경로에 mount하고 서비스를 다시 시작합니다.

### AI

- 활성화 여부와 OpenAI-compatible base URL
- API style(`responses` 또는 `chat_completions`)
- API key와 model
- 기본/최대 output token(1~262,144)
- 요청 timeout(10~3,600초)
- outbound 전체 허용 호스트(`allowedHosts`, 최대 64개)와 사설 주소 예외 hostname(`privateAllowedHosts`)

AI URL은 기본적으로 HTTPS만 허용됩니다. 폐쇄망에서 HTTP가 꼭 필요하면 관리 API의 `allowInsecureHttp`를 명시적으로 켜야 하며, 이 경우에도 `allowedHosts`에 정확히 등록한 내부 endpoint만 사용합니다. port 없는 허용 항목은 HTTP 80 또는 HTTPS 443에만 일치하고 비기본 port는 명시해야 합니다. 사설 주소를 해석하는 hostname은 OIDC와 같은 규칙으로 `privateAllowedHosts`에도 등록합니다. DNS 결과에 연결할 때 IP를 고정하고 모든 redirect를 다시 검사합니다. streaming이 기본이며 reverse proxy의 response buffering을 끕니다.

> **v0.1.0 업그레이드 필수 조치:** 기존에 사설 Keycloak/OIDC 또는 AI endpoint를 사용했더라도 `v0.1.4`은 이를 `privateAllowedHosts`로 자동 이관하지 않습니다. 업그레이드 전에 로컬 bootstrap 최고 관리자 로그인을 확인하고, 업그레이드 뒤 그 계정으로 각 설정의 정확한 DNS hostname을 `allowedHosts`와 `privateAllowedHosts`에 명시 저장한 다음 연결 테스트를 수행합니다. 이미 생성된 bootstrap 계정은 환경변수 비밀번호 변경으로 재설정되지 않습니다.

관리자 공통 system instruction, temperature와 사용자별 사용량 정책은 `v0.1.6` 설정 항목이 아닙니다. 필요한 경우 upstream AI gateway에서 적용하고, MOINA에 설정된 상한보다 더 좁은 한도로 운영합니다.

### 승인, 역할과 moderation

- 승인 정책 활성화, 대상 action과 approver 역할
- 변경 가능한 role/permission 묶음
- API/MCP key가 선택할 수 있는 permission 범위
- 신고 접수, 처리 상태와 사용자/게시물 제재

승인 정책이 비활성화되면 검토·승인·반려 상태와 메뉴를 제외합니다.

현재 승인 요청을 실제 생성하고 처리하는 Action은 `post.publish`입니다. 전역 `*`, 정확한 `post.publish` 또는 마지막 segment wildcard `post.*`를 사용할 수 있으며, 문법상 유효해도 아직 producer가 없는 `moim.member.approve`·`agent.post.publish` 같은 Action은 저장하지 않습니다. `post*`, `post.`, `post:*`, `*.publish`, `post..publish`도 저장할 수 없습니다. 선택한 모든 approver 역할은 최종 유효 권한으로 `approvals:review`를 가져야 하며 `*`와 `approvals:*`도 유효합니다. 승인 알림과 기존 대기 요청 접근 시에도 현재 역할·권한을 다시 검사하고 요청자의 자기 승인·반려를 금지합니다.

신고 유형·제재 단계·보존 기간을 관리자가 사용자 정의하는 정책 모델은 `v0.1.6`에 없습니다. 기관별 보존 규정은 PostgreSQL 백업·삭제 절차와 운영 문서로 관리합니다.

## 사설 CA

환경변수를 추가하지 않고 전체 PEM CA bundle을 표준 trust 경로에 mount합니다.

```bash
docker compose --env-file .env \
  -f deploy/docker-compose.offline.yml \
  -f deploy/docker-compose.private-ca.yml \
  up -d --pull never
```

`deploy/certs/ca-certificates.crt`에는 필요한 공개 루트, 사설 루트와 중간 인증서를 모두 포함합니다.

## 버전

`VERSION`, Go binary, React asset, `/api/v1/version`, 로그인 화면, 프로필 메뉴와 OCI label의 값은 모두 `v0.1.6`로 일치해야 합니다. 런타임 환경변수로 버전을 덮어쓰지 않습니다.
