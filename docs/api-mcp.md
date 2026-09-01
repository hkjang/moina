# MOINA REST API와 MCP

Canonical REST prefix는 `/api/v1`, MCP endpoint는 `/mcp`, 실시간 알림은 `/api/v1/ws/notifications`입니다. `/api/v1/mcp`와 `/api/v1/ws`도 호환 alias로 제공합니다. 정확한 request/response schema는 [`../api/openapi.yaml`](../api/openapi.yaml)을 기준으로 합니다.

## 인증

### 브라우저 session

```http
POST /api/v1/auth/login
Content-Type: application/json

{"username":"user","password":"..."}
```

응답의 HttpOnly session cookie를 사용합니다. unsafe method에는 `moina_csrf` cookie 값과 같은 `X-CSRF-Token` header가 필요합니다.

### 개인 API/MCP key

```http
Authorization: Bearer mk_...
```

키는 사용자별로 만들고 필요한 permission과 만료일만 부여합니다. 원문은 한 번만 표시됩니다. URL, query, log와 source code에 넣지 않습니다.

## 주요 REST resource

| 영역 | 대표 endpoint |
| --- | --- |
| 상태 | `GET /healthz`, `GET /readyz`, `GET /api/v1/version`, `GET /metrics` |
| 인증 | `POST /auth/login`, `GET /auth/me`, `POST /auth/logout`, OIDC start/callback |
| Moin | `GET/POST /posts`, `GET/PATCH/DELETE /posts/{id}`, reaction, Echo, Remoin |
| 관계 | Link/unlink, follower/following 목록 |
| Flow | For Me/Following `limit`/opaque `cursor` 키셋 pagination과 추천 이유 |
| 발견 | trigram·전문 검색, topics, pulse, moims |
| 개인 | profile, preferences, sessions, API/MCP keys와 rotation |
| 운영 | users, roles, settings, OIDC, AI, approvals, reports, audit와 Outbox 복구 |

Flow 이외의 관리 collection은 `limit`과 숫자 `offset`을 사용합니다. Flow 응답의 `nextCursor`는 versioned Base64 URL로 인코딩한 opaque 값이며 client가 해석하거나 변경해서는 안 됩니다. Following cursor는 게시 시각·ID를 보존합니다. For Me cursor는 기준 시각·점수·ID·랭킹 버전과 사용자별 server ranking snapshot ID를 포함합니다.

For Me 첫 페이지는 필터를 통과한 최근 후보 최대 200개의 합계·component 점수와 당시 개인화 설정을 동결합니다. 동일 사용자·랭킹 버전·설정은 같은 30초 bucket에서 snapshot을 재사용합니다. 시간 만료는 한 시간이지만 사용자당 활성 snapshot이 최대 3개이므로 반복 refresh 시 이전 값이 조기 제거될 수 있습니다. 잘못된 cursor는 `invalid_cursor`, 정책 버전이 달라진 cursor는 `ranking_version_mismatch`, 만료되거나 제거된 snapshot은 `feed_snapshot_expired` 오류가 됩니다. 이 경우 client는 저장한 페이지와 cursor를 버리고 첫 페이지부터 다시 조회합니다.

같은 사용자의 For Me 첫 페이지 생성이 이미 진행 중이면 서버는 lock을 기다려 요청을 쌓지 않고 HTTP `429`, `feed_snapshot_busy`와 `Retry-After: 1`을 반환합니다. Client는 header의 초만큼 기다린 뒤 **첫 페이지 요청만 한 번 다시 시도**하며 기존 cursor를 붙이지 않습니다.

오류는 HTTP status와 함께 다음 형태를 사용합니다.

```json
{
  "code": "permission_denied",
  "message": "이 작업을 수행할 권한이 없습니다."
}
```

### Moin 미디어와 대체 텍스트

`POST /api/v1/media`의 multipart `file`은 이미지·MP4·WebM을 streaming 업로드하며 선택적인 `altText` 또는 `alt` text를 함께 받을 수 있습니다. 이 값은 업로드 기본 설명입니다. Moin 작성 시 이미 업로드한 media ID와 문맥별 최종 대체 텍스트를 함께 확정하고 `post_media` 관계에 저장합니다.

작성 client는 `posts:write` 권한으로 업로드 전에 현재 서버 계약을 조회합니다.

```http
GET /api/v1/media/config
Authorization: Bearer mk_...
```

```json
{
  "data": {
    "maxUploadBytes": 10485760,
    "maxPerPost": 4,
    "acceptedTypes": [
      "image/jpeg", "image/png", "image/gif", "image/webp", "video/mp4", "video/webm"
    ]
  }
}
```

Client는 이 값을 파일 선택 UI와 사전 검사에 사용하되 업로드 API의 서버 검증을 최종 기준으로 삼습니다. 미사용 업로드 보존 시간인 `orphanTtlHours`는 관리자 전용 설정이므로 이 응답에 포함되지 않습니다.

사용자별 미첨부 media는 최대 100개·512 MiB입니다. `POST /api/v1/media`가 HTTP `429`를 반환하면 응답 code는 `media_quota_exceeded`이며, 기존 업로드를 Moin·프로필에 연결하거나 orphan TTL 정리를 기다린 뒤 재시도합니다. 이 고정 quota는 `/media/config` 응답이나 관리자 변경 항목이 아닙니다.

```json
{
  "content": "폐쇄망 운영 화면입니다.",
  "mediaIds": ["media_01"],
  "mediaAltTexts": {
    "media_01": "MOINA 운영 대시보드에서 데이터베이스와 알림 상태가 정상으로 표시된 화면"
  }
}
```

`mediaAltTexts` key는 같은 요청의 `mediaIds`에 포함돼야 하고 각 값은 최대 500자입니다. 조회 응답의 각 `media` 항목은 해당 Moin 관계의 `altText`를 제공합니다. 같은 media ID를 두 Moin에 연결해 서로 다른 설명을 저장해도 한쪽 값이 다른 쪽을 덮어쓰지 않습니다. 게시물을 수정할 때도 `PATCH /api/v1/posts/{postID}`의 `content`와 선택적 `mediaAltTexts`로 그 Moin의 연결 설명만 변경할 수 있습니다.

## 개인 알림 설정

`GET /api/v1/profile/preferences`는 appearance, feed와 다음 완전한 알림 문서를 반환합니다. `PUT`은 부분 section만 보내도 나머지 값을 보존합니다.

```json
{
  "notifications": {
    "inApp": {"mentions": true, "signals": true, "follows": true, "echoes": true, "approvals": true},
    "toast": {"enabled": true},
    "desktop": {"enabled": false},
    "digest": {"mode": "daily", "time": "08:00"},
    "quietHours": {"enabled": true, "start": "22:00", "end": "07:00"}
  }
}
```

`digest.mode`는 `off`, `hourly`, `daily` 중 하나이고 시각은 서비스 기본 시간대의 `HH:MM`입니다. Digest를 새로 켜거나 mode·일별 시각을 바꾸면 worker가 변경을 감지한 시점부터 새 집계 구간을 시작하며, 꺼져 있던 기간이나 이전 일정의 알림을 한꺼번에 재생하지 않습니다. Worker는 1분 간격으로 설정을 확인합니다. In App의 Signal·Mention·Follow·Echo는 알림 센터 노출과 미확인 수 포함 여부를 제어합니다. In App을 끈 유형도 cross-instance Toast/Desktop fanout을 위해 `inApp=false`와 읽음 상태의 durable 전달 row로 저장하며 목록에서는 숨깁니다. 승인·보안 알림은 운영상 필수이므로 `approvals`를 false로 보내도 true로 정규화됩니다. Toast와 Desktop은 독립 실시간 표시 채널이며 조용한 시간에는 보류되지만 durable 전달 기록은 유지됩니다. Desktop은 이 설정과 별도로 사용자 동작으로 Browser Notification 권한을 허용해야 합니다.

## AI SSE

AI 요청은 기본 streaming입니다.

```http
POST /api/v1/ai/chat
Authorization: Bearer mk_...
Accept: text/event-stream
Content-Type: application/json

{"messages":[{"role":"user","content":"이 Chain을 요약해줘"}],"maxTokens":8192}
```

`maxTokens`는 1~262,144 범위이면서 관리자가 설정한 상한과 실제 model 상한 이하여야 합니다. Client disconnect는 upstream 요청 취소로 전달합니다. MOINA는 upstream의 SSE byte stream을 buffering 없이 전달하므로 event 형식은 선택한 API style과 공급자 계약을 따릅니다.

OIDC와 AI 관리 설정의 `allowedHosts`에는 정확한 DNS 이름/IP 또는 `host:port`를 넣습니다. port 없는 값은 URL scheme의 기본 port(HTTPS 443, HTTP 80)에만 일치합니다. RFC1918/ULA로 해석되는 내부 DNS hostname은 같은 authority를 `allowedHosts`와 `privateAllowedHosts` 양쪽에 등록합니다. `privateAllowedHosts`에는 IP literal을 넣을 수 없고, loopback·link-local·cloud metadata·CGNAT·unspecified·multicast는 어떤 설정으로도 허용되지 않습니다.

`service.general.publicBaseUrl`은 path/query/fragment 없는 외부 HTTP(S) origin입니다. OIDC의 명시적 `redirectUrl`이 없으면 이 값으로 callback을 만들며, 관리자 OIDC 조회 응답의 `effectiveRedirectUrl`에서 실제 등록할 주소를 확인할 수 있습니다. `defaultRedirectUrl`은 직접 지정 override를 제거했을 때의 주소이고 `redirectUrlSource`와 `defaultRedirectUrlSource`는 각각의 계산 출처입니다. 연결 테스트는 Discovery뿐 아니라 authorization endpoint에 PKCE `prompt=none` 사전 요청을 보내 실제 Redirect URI 허용 여부도 확인하며, Keycloak이 거부하면 `oidc_redirect_rejected`를 반환합니다. 그 밖에 `oidc_private_host_denied`, `oidc_egress_denied`, `oidc_dns_failed`, `oidc_tls_failed`, `oidc_timeout`, `oidc_issuer_mismatch`, `oidc_authorization_failed`를 구분합니다.

`v0.1.0`의 기존 사설 OIDC·AI 설정은 업그레이드 후 자동으로 `privateAllowedHosts`를 얻지 않습니다. 로컬 bootstrap 최고 관리자로 로그인해 각 hostname을 명시 저장하고 관리 API 연결 테스트를 통과시켜야 합니다. Bootstrap 환경변수의 비밀번호 변경은 이미 생성된 로컬 계정을 재설정하지 않습니다.

## WebSocket 알림

브라우저 session으로 `/api/v1/ws/notifications`에 연결합니다. Server는 handshake에서 Origin과 권한을 검증합니다. 알림 JSON의 `inApp`, `toast`, `desktop` boolean은 각 전달 채널의 현재 정책을 나타냅니다. Client는 Toast/Desktop이 true인 채널만 표시하고 `inApp`은 알림 센터 노출 여부를 뜻합니다. Browser queue가 포화된 느린 socket은 서버가 종료하고 client는 최대 30초 지수 backoff로 재연결합니다. Client는 연결 직후와 60초마다 `GET /api/v1/notifications`의 unread summary를 다시 조회해 LISTEN/WebSocket 공백을 보완합니다. REST 목록과 unread 수에는 `inApp=true`인 row만 포함합니다. `v0.1.9` WebSocket 자체는 last event ID replay를 제공하지 않습니다.

```json
{
  "id": "ntf_example",
  "type": "signal",
  "payload": {},
  "inApp": false,
  "toast": true,
  "desktop": false,
  "createdAt": "2026-08-30T00:00:00Z"
}
```

이 예시는 알림 센터에서는 숨기되 현재 WebSocket을 받은 앱에는 Toast로 표시하는 독립 채널 정책입니다.

## 운영 API

Prometheus collector는 `GET /metrics`에서 `text/plain; version=0.0.4`를 읽습니다. 이 endpoint는 사용자별 label이나 SQL문을 제공하지 않지만 외부 공개를 전제로 하지 않으므로 reverse proxy에서 운영 수집망으로 제한합니다. 모든 HTTP 응답의 `X-Request-ID`는 같은 요청의 구조화 log `request_id`와 일치합니다.

`network.proxy` 관리자 설정은 `trustedProxies` 배열에 정확한 직접 Peer IP 또는 CIDR을 최대 128개 받습니다. Peer가 이 목록에 있을 때만 `Forwarded` 또는 `X-Forwarded-For`·`X-Forwarded-Proto`를 신뢰합니다. Client chain은 오른쪽부터 검증하고 protocol은 가장 가까운 오른쪽 hop만 사용합니다. 감사 detail은 `socketIp`, 계산된 `clientIp`, 전체 `proxyChain`을 분리합니다. 로그인·가입과 개인 API/MCP key 요청 한도는 PostgreSQL bucket으로 계산해 여러 인스턴스가 같은 quota를 공유하며 저장소 오류는 HTTP 503 `rate_limit_unavailable`로 반환합니다.

시간별·일별 Digest는 Outbox event 생성 시각이 아닌 `notifications.delivered_at` 순서로 아직 `digested_at`이 없는 알림을 집계합니다. 처리한 row는 Digest 생성과 같은 transaction에서 표시되므로 Outbox 처리가 지연되거나 worker 집계 도중 새로 저장된 알림도 다음 실행에서 포함됩니다. `config_signature`은 구독 mode와 일별 시각 전환을 식별하며 전환 시 현재 경계 이전 row를 처리 완료로 표시해 과거 backlog를 재생하지 않습니다. Advisory lock과 사용자별 상태는 여러 인스턴스의 중복 요약을 막고, 잘못된 저장 설정은 사용자별 savepoint에서 격리됩니다.

현재 승인 요청 producer와 reviewer가 구현된 Action은 `post.publish`입니다. 이를 포함하는 `*`, exact `post.publish` 또는 `post.*`만 저장할 수 있습니다. 문법상 유효해도 구현되지 않은 Action은 HTTP 400 `unsupported_actions`, `post*`, `post.`, `post:*`, `*.publish`, `post..publish`는 `invalid_actions`입니다. 모든 `approverRoles`가 최종 유효 권한 `approvals:review`를 가져야 하며 `*`와 `approvals:*`도 허용됩니다. 유효·무효 역할을 섞어 보내면 전체 요청이 거부됩니다. Pending 요청 조회·승인·반려 때도 현재 권한을 다시 검사하고 요청자 자신의 승인·반려는 409로 차단합니다.

재시도 한도를 넘은 Transactional Outbox 이벤트는 감사 권한이 있는 관리자가 조회합니다.

```http
GET /api/v1/admin/outbox?status=dead_letter&limit=100
```

응답 항목에는 event ID·type·aggregate ID, 처리용 JSON payload, 시도/최대 시도 수, 마지막 오류·시각과 Dead Letter 시각이 포함됩니다. 관리자 UI는 payload 원문을 렌더링하지 않지만 조회 API는 `admin:access`와 `audit:read` 권한으로 보호되므로 해당 권한을 최소 인원에게만 부여합니다. 원인을 해결한 뒤 `admin:access`와 별도 `outbox:manage` 권한이 있는 관리자가 다음 API로 시도 수와 lease를 초기화하고 즉시 재처리 대기 상태로 돌립니다.

```http
POST /api/v1/admin/outbox/{eventID}/retry
X-CSRF-Token: ...
```

재처리는 감사 로그에 기록됩니다. 동일 event의 업무 효과는 idempotency key와 대상 notification ID로 중복을 방지합니다. 조사만 하는 역할에는 `audit:read`만, 실제 복구 역할에는 `outbox:manage`를 추가해 최소 권한을 유지합니다.

## MCP

MCP Streamable HTTP 요청은 JSON-RPC 2.0과 Bearer key를 사용합니다.

`v0.1.9`은 stateless POST JSON-RPC 전송만 제공합니다. `GET /mcp`와 `GET /api/v1/mcp`는 capability 확인 요청에 `405 Method Not Allowed`와 `Allow: POST`를 반환하며 별도 SSE channel을 열지 않습니다.

```bash
curl --fail --silent http://127.0.0.1:8080/mcp \
  -H 'Authorization: Bearer mk_REDACTED' \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2025-11-25' \
  --data '{
    "jsonrpc":"2.0",
    "id":1,
    "method":"initialize",
    "params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"example","version":"1"}}
  }'
```

대표 tool namespace:

| Tool | Permission | 설명 |
| --- | --- | --- |
| `moina.flow.read` | `posts:read` | 개인 Flow 조회 |
| `moina.posts.get` | `posts:read` | ID로 Moin 조회 |
| `moina.posts.search` | `posts:read` | 사람·Moin·Topic·Moim 키워드 검색 |
| `moina.posts.create` | `posts:write` | Moin 작성 |
| `moina.echo.create` | `posts:write` | Echo 작성 |
| `moina.topics.list` | `posts:read` | Topic 탐색 |
| `moina.notifications.list` | `posts:read` | 알림 조회 |
| `moina.profile.get` | `posts:read` | 허용된 프로필 조회 |
| `moina.ai.status` | `ai:use` | AI 공급자 공개 상태 조회 |

MCP가 호출하는 동작도 REST와 같은 service method, permission, 승인 정책, rate limit과 audit를 사용합니다. 승인 정책의 대상인 Moin 작성은 즉시 공개하지 않고 `pending_approval` 상태의 Moin 정보를 반환합니다.

## Rotation 예시

```http
POST /api/v1/profile/keys/{keyId}/rotate
X-CSRF-Token: ...
```

성공 응답의 새 token은 한 번만 표시됩니다. 기존 token은 같은 transaction에서 즉시 무효화됩니다.
