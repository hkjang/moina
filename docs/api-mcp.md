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
| 상태 | `GET /healthz`, `GET /readyz`, `GET /api/v1/version` |
| 인증 | `POST /auth/login`, `GET /auth/me`, `POST /auth/logout`, OIDC start/callback |
| Moin | `GET/POST /posts`, `GET /posts/{id}`, reaction, Echo, Remoin |
| 관계 | Link/unlink, follower/following 목록 |
| Flow | For Me/Following `limit`/`offset` pagination과 추천 이유 |
| 발견 | search, topics, pulse, moims |
| 개인 | profile, preferences, sessions, API/MCP keys와 rotation |
| 운영 | users, roles, settings, OIDC, AI, approvals, reports, audit |

Collection은 `limit`과 숫자 `offset`을 사용하고 일부 응답은 다음 offset을 문자열 `nextCursor`로 제공합니다. 오류는 HTTP status와 함께 다음 형태를 사용합니다.

```json
{
  "code": "permission_denied",
  "message": "이 작업을 수행할 권한이 없습니다."
}
```

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

## WebSocket 알림

브라우저 session으로 `/api/v1/ws/notifications`에 연결합니다. Server는 handshake에서 Origin과 권한을 검증합니다. 연결이 끊긴 동안의 알림은 재연결 후 `GET /api/v1/notifications`로 다시 조회합니다. `v0.1.0` WebSocket 자체는 last event ID replay를 제공하지 않습니다.

## MCP

MCP Streamable HTTP 요청은 JSON-RPC 2.0과 Bearer key를 사용합니다.

`v0.1.0`은 stateless POST JSON-RPC 전송만 제공합니다. `GET /mcp`와 `GET /api/v1/mcp`는 capability 확인 요청에 `405 Method Not Allowed`와 `Allow: POST`를 반환하며 별도 SSE channel을 열지 않습니다.

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
