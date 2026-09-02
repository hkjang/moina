# MOINA 설정 가이드

이 문서는 `v0.1.15`의 환경변수 계약과 관리자 화면에서 변경할 수 있는 설정을 구분합니다.

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

새 미디어는 PostgreSQL Large Object로 일정한 메모리 사용량 안에서 업로드·다운로드되며, Moin이나 프로필에 연결되지 않은 업로드는 설정된 TTL이 지나면 내장 정리 작업이 삭제합니다. 기존 `bytea` 미디어는 호환 읽기를 유지합니다. 사용자별 미첨부 quota 100개·512 MiB, 인스턴스당 동시 Large Object read 최대 8개와 cleaner 한 주기 최대 10,000개는 `v0.1.15`의 고정 안전 한도이며 관리자 설정으로 변경할 수 없습니다. DB pool이 작으면 media read보다 일반 API 연결 5개를 우선 남깁니다.

업로드 시 입력한 대체 텍스트는 재사용 기본값입니다. Moin 작성·수정 시 최종 대체 텍스트를 `post_media` 관계에 따로 저장하므로 같은 media ID를 여러 Moin에서 사용해도 각 문맥의 설명이 서로 덮어쓰이지 않습니다.

작성 화면과 개인 프로필 이미지 설정은 인증 후 `GET /api/v1/media/config`를 조회해 현재 `maxUploadBytes`와 `acceptedTypes`를 적용하고, 작성 화면은 `maxPerPost`도 적용합니다. 프로필 이미지는 JPEG·PNG·GIF·WebP 한 장만 허용하며 파일 선택·끌어 놓기·클립보드 붙여넣기를 지원합니다. 이 응답에는 관리자 전용 보존 값인 `orphanTtlHours`가 포함되지 않습니다. 설정을 바꾼 뒤 이미 화면을 열어 둔 사용자는 업로드 전에 이 계약을 다시 조회해야 하며, 서버 검증이 최종 기준입니다.

`publicBaseUrl`은 path/query/fragment가 없는 외부 HTTP(S) origin입니다. OIDC 전용 `redirectUrl`이 비어 있을 때 `${publicBaseUrl}/api/v1/auth/oidc/callback`을 사용하며, 둘 다 비어 있을 때만 신뢰된 현재 요청 주소로 계산합니다. 기존 `redirectUrl` 직접 지정값은 `publicBaseUrl`보다 우선하므로 관리자 OIDC 화면의 경고가 보이면 **사이트 기본 주소 사용**으로 이전 override를 제거합니다. 화면에 표시되는 **실제 로그인 요청 Redirect URI**를 Keycloak Client의 **Valid redirect URIs**에 공백 없이 그대로 등록해야 합니다. 동시 session 수, Moin 글자 수, WebSocket 및 검색 정책은 `v0.1.15` 관리자 설정 모델에 포함되지 않습니다.

### 보존 기간

`service.retention` 설정은 트래픽에 비례해 늘어나는 운영 테이블의 상한을 정합니다. 관리자 설정 API의 `PUT /api/v1/admin/settings/service.retention`으로 변경합니다.

| 키 | 대상 | 기본값 |
| --- | --- | --- |
| `auditDays` | `audit_events` | `0` (무기한 보관) |
| `notificationDays` | `notifications` | `90` |
| `outboxDays` | 전달 완료된 `outbox_events` | `14` |
| `aiUsageDays` | `ai_usage_events` | `180` |

각 값은 0일부터 3650일 사이여야 하며 `0`은 해당 테이블을 정리하지 않는다는 뜻입니다. 감사 기록은 사후 제출 요구가 가장 많은 자료이므로 업그레이드가 스스로 삭제하지 않도록 기본값이 무기한이며, 관리자가 `auditDays`를 지정할 때만 정리합니다. 전달에 실패해 Dead Letter로 남은 Outbox event는 보존 기간과 무관하게 유지되므로 관리자 재처리 대상이 사라지지 않습니다.

만료된 session row는 이 설정과 무관하게 항상 정리합니다. 만료 session은 이미 인증에 실패하므로 보관할 이유가 없습니다.

### 설정 캐시

관리자 설정과 역할 권한은 각 인스턴스 메모리에 최대 30초 캐시합니다. 설정을 저장하면 `pg_notify`로 모든 인스턴스에 즉시 알려 캐시를 버리므로, 정상 상태에서는 변경이 곧바로 반영됩니다. 30초 TTL은 알림을 놓쳤을 때(예: LISTEN 연결 재접속 중)의 상한이며, 재접속 직후에는 캐시 전체를 버리고 다시 읽습니다.

개인 API·MCP 키의 최근 사용 시각은 인스턴스당 키별로 분당 최대 1회 기록합니다. 이전에는 모든 API 요청이 읽기임에도 쓰기를 발생시켰습니다. 관리자 화면의 최근 사용 표시는 분 단위 정밀도를 가집니다.

### 검색

검색어가 있으면 trigram·전문 검색 index로 후보를 먼저 좁힌 뒤 관련도 순으로 정렬합니다. 검색어 없이 `recommended=true`만 지정하면(멘션 자동완성의 추천 목록 등) 관련도 점수가 모든 행에서 0이므로 계산을 생략하고 Link 수 순으로 바로 정렬합니다.

`offset`은 `limit`과 함께 모든 검색 대상에 적용됩니다. 응답에는 적용된 `limit`과 `offset`이 함께 담깁니다.

### 응답 압축과 본문 읽기 기한

`Accept-Encoding: gzip`을 보낸 client에게 SPA 번들, CSS와 JSON 응답을 gzip으로 전달합니다. `text/event-stream` AI 스트리밍, 이미지·동영상과 `Range` 요청은 압축하지 않고 원본 바이트를 그대로 보냅니다. 별도 설정은 없습니다.

요청 본문 읽기 기한은 종류에 따라 다릅니다. `POST /api/v1/media`는 15분, 나머지 요청은 30초이며, header 읽기는 10초로 제한합니다. 최대 50 MiB 업로드를 30초 안에 받으려면 지속 14 Mbps가 필요하므로 업로드에만 별도 기한을 둡니다.

### Reverse Proxy와 요청 한도

`network.proxy.trustedProxies`에는 MOINA에 직접 연결하는 Proxy Peer의 IP 또는 CIDR만 입력합니다. 직접 연결 Peer가 목록에 있을 때만 표준 `Forwarded` 또는 `X-Forwarded-For`·`X-Forwarded-Proto`를 신뢰하며, 오른쪽부터 신뢰 Proxy를 제거해 실제 Client IP를 계산합니다. Proxy가 `Forwarded`·`X-Forwarded-For`를 하나로 합치지 않고 별도 header 줄로 덧붙여도 받은 순서대로 이어 붙인 하나의 chain으로 처리하므로, Client가 미리 보낸 줄이 chain 전체를 대신할 수 없습니다. 전달 protocol은 가장 가까운 오른쪽 hop의 값만 사용해 왼쪽의 사용자 입력이 secure cookie 판단을 바꾸지 못하게 합니다. 빈 목록은 모든 전달 헤더를 무시하는 안전한 기본값입니다. 감사 기록에는 `socketIp`, `clientIp`, `proxyChain`을 분리해 남깁니다.

로그인은 사용자명+Client IP 5분당 5회와 사용자명 전체 5분당 20회를 함께 제한하고, 가입은 Client IP 기준 10분당 5회입니다. 개인 API·MCP key의 `rateLimitPerMinute`도 PostgreSQL `rate_limit_buckets`를 사용하므로 여러 MOINA 인스턴스가 같은 quota를 공유합니다. Rate Limit 저장소를 사용할 수 없으면 인증·키 요청을 허용하지 않고 `rate_limit_unavailable`을 반환합니다.

### Keycloak/OIDC

- 활성화 여부
- issuer URL
- client ID와 client secret
- 고급 직접 지정 redirect URL, 실제·기본 callback과 각 계산 출처
- scopes, claim mapping과 기본 역할
- 자동 사용자 생성 여부
- outbound 전체 허용 호스트(`allowedHosts`), 사설 주소 예외 hostname(`privateAllowedHosts`)과 폐쇄망 HTTP 허용 여부

Client secret은 암호화해 저장하고 API 응답에 반환하지 않습니다. 빈 값은 기존 secret 유지, 명시적 삭제 동작은 제거로 구분합니다.

`allowedHosts`에는 DNS 이름 또는 IP를 정확히 입력합니다. port 없는 항목은 URL scheme의 기본 port(HTTPS 443, HTTP 80)에만 일치하므로 8443 같은 비기본 port는 `host:port`로 등록해야 합니다. scheme, 경로, 사용자 정보와 wildcard는 허용하지 않습니다. 기존 설정에서 목록이 비어 있으면 현재 issuer의 정확한 host와 비기본 port만 초기값으로 채웁니다.

DNS가 RFC1918 IPv4 또는 ULA IPv6를 반환하는 폐쇄망 endpoint는 정확한 DNS hostname을 `privateAllowedHosts`에도 등록합니다. 같은 authority가 `allowedHosts`에 먼저 있어야 하며, 비기본 port라면 두 목록 모두 같은 `host:port`를 사용합니다. IP literal은 사설 주소 예외 목록에 넣지 않습니다. loopback, link-local, cloud metadata, CGNAT, unspecified와 multicast 주소는 `privateAllowedHosts`로도 열 수 없습니다. 저장·연결 테스트와 실제 로그인 중 최초 URL, discovery가 제공한 endpoint, redirect, DNS 해석 결과를 매 단계 다시 검증합니다.

관리자 연결 테스트는 차단된 정확한 hostname 또는 host:port, MOINA 컨테이너의 DNS 결과와 연결 단계를 표시합니다. RFC1918/ULA이면 `oidc_private_host_required`와 함께 같은 값을 두 Host 목록에 자동 입력할 수 있습니다. `127.0.0.0/8`, `::1`, link-local, cloud metadata, CGNAT과 예약·특수 용도 주소는 `oidc_address_forbidden`이며 목록에 등록해도 열리지 않으므로 Keycloak endpoint 또는 DNS를 컨테이너에서 도달 가능한 RFC1918/ULA나 공인 주소로 변경합니다. 이 상세 정보는 `settings:manage` 권한의 연결 테스트에만 반환되고 공개 로그인 오류에는 내부 DNS 결과를 노출하지 않습니다. Discovery 성공 뒤 authorization endpoint에 PKCE와 `prompt=none`으로 안전한 사전 요청을 보내 Keycloak Client가 실제 Redirect URI를 허용하는지도 확인합니다. 거부되면 `oidc_redirect_rejected`, 그 밖의 authorization 사전 확인 실패는 `oidc_authorization_failed`로 구분합니다. 사전 요청은 MOINA Callback redirect를 따라가지 않습니다. Issuer URL은 discovery 문서의 `issuer`와 정확히 일치해야 하며 공급자가 trailing slash를 사용하는 경우 그대로 입력합니다. 내부 CA는 전체 PEM bundle을 표준 trust 경로에 mount하고 서비스를 다시 시작합니다.

### AI

- 활성화 여부와 OpenAI-compatible base URL
- API style(`responses` 또는 `chat_completions`)
- API key와 model
- 기본/최대 output token(1~262,144)
- 요청 timeout(10~3,600초)
- outbound 전체 허용 호스트(`allowedHosts`, 최대 64개)와 사설 주소 예외 hostname(`privateAllowedHosts`)

AI URL은 기본적으로 HTTPS만 허용됩니다. 폐쇄망에서 HTTP가 꼭 필요하면 관리 API의 `allowInsecureHttp`를 명시적으로 켜야 하며, 이 경우에도 `allowedHosts`에 정확히 등록한 내부 endpoint만 사용합니다. port 없는 허용 항목은 HTTP 80 또는 HTTPS 443에만 일치하고 비기본 port는 명시해야 합니다. 사설 주소를 해석하는 hostname은 OIDC와 같은 규칙으로 `privateAllowedHosts`에도 등록합니다. DNS 결과에 연결할 때 IP를 고정하고 모든 redirect를 다시 검사합니다. streaming이 기본이며 reverse proxy의 response buffering을 끕니다.

> **v0.1.0 업그레이드 필수 조치:** 기존에 사설 Keycloak/OIDC 또는 AI endpoint를 사용했더라도 `v0.1.4`은 이를 `privateAllowedHosts`로 자동 이관하지 않습니다. 업그레이드 전에 로컬 bootstrap 최고 관리자 로그인을 확인하고, 업그레이드 뒤 그 계정으로 각 설정의 정확한 DNS hostname을 `allowedHosts`와 `privateAllowedHosts`에 명시 저장한 다음 연결 테스트를 수행합니다. 이미 생성된 bootstrap 계정은 환경변수 비밀번호 변경으로 재설정되지 않습니다.

관리자 공통 system instruction, temperature와 사용자별 사용량 정책은 `v0.1.15` 설정 항목이 아닙니다. 필요한 경우 upstream AI gateway에서 적용하고, MOINA에 설정된 상한보다 더 좁은 한도로 운영합니다.

### SMTP 메일과 알림

- 활성화 여부, SMTP DNS 이름과 port
- STARTTLS, implicit TLS(SMTPS) 또는 폐쇄망 무인증 연결
- 사용자 이름과 암호화해 저장하는 password
- 보내는 이메일·이름과 3~60초 연결 제한 시간
- 설정한 정확한 SMTP DNS 이름에 대한 사설망 연결 허용 여부

SMTP password는 OIDC·AI secret과 같은 `MOINA_ENCRYPTION_KEY`로 암호화하며 조회 API는 `passwordConfigured`만 반환합니다. **저장 후 테스트 메일**은 현재 관리자의 프로필 이메일로 실제 SMTP 전송을 확인합니다. SMTP host에는 port를 섞지 않고 정확한 DNS 이름을 입력합니다. 사설망 허용은 입력한 `host:port` 하나에만 적용하며 DNS 이름이 RFC1918/ULA로 해석되는 경우만 엽니다. IP literal 사설 예외, loopback, link-local, metadata와 CGNAT은 허용하지 않습니다. `none` 연결은 폐쇄망의 무인증 relay에만 사용할 수 있고 인증 정보는 보낼 수 없습니다.

사용자는 프로필에 올바른 수신 주소를 저장한 뒤 **알림 개인화 → 이메일 알림**에서 수신을 명시적으로 켭니다. 선택한 멘션·Signal·Link·Echo 유형이 이메일에도 공통 적용됩니다. Digest가 켜져 있으면 일반 활동은 시간별·일별 요약으로 묶고 멘션·승인·보안은 즉시 전달합니다. 메일 전송은 게시 transaction과 분리된 `notification.email` Outbox 이벤트이므로 SMTP 장애가 Moin 작성을 실패시키지 않으며, 재시도 한도를 넘은 이벤트는 관리자 실패 이벤트 복구 화면에 남습니다. 이메일을 제거한 계정의 이벤트는 재시도하지 않습니다.

### 승인, 역할과 moderation

- 승인 정책 활성화, 대상 action과 approver 역할
- 변경 가능한 role/permission 묶음
- API/MCP key가 선택할 수 있는 permission 범위
- 신고 접수, 처리 상태와 사용자/게시물 제재

승인 정책이 비활성화되면 검토·승인·반려 상태와 메뉴를 제외합니다.

현재 승인 요청을 실제 생성하고 처리하는 Action은 `post.publish`입니다. 전역 `*`, 정확한 `post.publish` 또는 마지막 segment wildcard `post.*`를 사용할 수 있으며, 문법상 유효해도 아직 producer가 없는 `moim.member.approve`·`agent.post.publish` 같은 Action은 저장하지 않습니다. `post*`, `post.`, `post:*`, `*.publish`, `post..publish`도 저장할 수 없습니다. 선택한 모든 approver 역할은 최종 유효 권한으로 `approvals:review`를 가져야 하며 `*`와 `approvals:*`도 유효합니다. 승인 알림과 기존 대기 요청 접근 시에도 현재 역할·권한을 다시 검사하고 요청자의 자기 승인·반려를 금지합니다.

신고 유형·제재 단계·보존 기간을 관리자가 사용자 정의하는 정책 모델은 `v0.1.15`에 없습니다. 기관별 보존 규정은 PostgreSQL 백업·삭제 절차와 운영 문서로 관리합니다.

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

`VERSION`, Go binary, React asset, `/api/v1/version`, 로그인 화면, 프로필 메뉴와 OCI label의 값은 모두 `v0.1.15`로 일치해야 합니다. 런타임 환경변수로 버전을 덮어쓰지 않습니다.
