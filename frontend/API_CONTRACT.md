# Frontend REST 계약

모든 경로의 기준 prefix는 `/api/v1`입니다. 성공 JSON은 `{ "data": ... }`, 오류는 `{ "code": "...", "message": "..." }`이며 클라이언트가 성공 envelope만 해제합니다. 브라우저 세션 cookie는 `moina_session`입니다. `POST`, `PUT`, `PATCH`, `DELETE`에는 `moina_csrf` cookie 값을 `X-CSRF-Token`으로 전송하고 Bearer API key 요청에는 붙이지 않습니다.

## 공개·인증

| Method | Path | 용도 |
| --- | --- | --- |
| GET | `/version` | 로그인·프로필 메뉴 버전 |
| POST | `/auth/login` | 로컬 로그인 |
| POST | `/auth/register` | 관리자가 허용한 로컬 가입 |
| GET | `/auth/oidc/status` | OIDC와 가입 허용 상태 |
| GET | `/auth/oidc/login`, `/auth/oidc/callback` | Keycloak OIDC |
| GET | `/auth/me` | 현재 세션 |
| POST | `/auth/logout` | 로그아웃 |

## 소셜·개인화

| Method | Path |
| --- | --- |
| GET | `/feed?mode=for_me\|following` |
| GET, POST | `/posts` |
| GET, PATCH, DELETE | `/posts/{id}` |
| GET | `/posts/{id}/replies` |
| POST, DELETE | `/posts/{id}/reactions`, `/posts/{id}/bookmark`, `/posts/{id}/remoin` |
| GET | `/users/{username}`, `/topics`, `/topics/{slug}`, `/search`, `/notifications`, `/moims`, `/moims/{slug}` |
| POST, DELETE | `/links/{userId}`, `/topics/{slug}/follow`, `/moims/{slug}/members` |
| POST | `/notifications/read`, `/moims`, `/media`, `/reports` |
| GET, PATCH | `/profile` |
| GET, PUT | `/profile/preferences` |
| POST | `/profile/password` |
| GET, POST | `/profile/keys` |
| PATCH, DELETE | `/profile/keys/{keyId}` |
| POST | `/profile/keys/{keyId}/rotate` |
| GET | `/workflow/status`, `/approvals`, `/ai/status` |
| POST | `/approvals/{id}/approve`, `/approvals/{id}/reject` |
| POST (SSE) | `/ai/chat` |
| WebSocket | `/ws/notifications` |

게시물 작성 body는 `content`, `visibility`, `mediaIds`와 선택적인 `replyToId`, `quoteMoinId`를 사용합니다. AI 응답은 기본 SSE 스트리밍이며 `maxTokens`는 관리자가 정한 범위에서 최대 262,144입니다. WebSocket의 최초 `{type:"connected"}` 이벤트는 알림으로 표시하지 않습니다.

## 서비스 관리자

| Method | Path |
| --- | --- |
| GET | `/admin/stats`, `/admin/users`, `/admin/posts`, `/admin/reports`, `/admin/roles`, `/admin/audit` |
| POST | `/admin/users` |
| PATCH | `/admin/users/{id}`, `/admin/posts/{id}`, `/admin/reports/{id}` |
| GET, PUT | `/admin/oidc`, `/admin/ai`, `/admin/workflow` |
| POST | `/admin/oidc/test`, `/admin/ai/test` |
| GET | `/admin/settings` |
| PUT | `/admin/settings/{key}` |
| PUT | `/admin/roles` |

일반 설정 key는 `service.general`, `api.access`, `media.config`입니다. OIDC와 AI 비밀 값은 조회하지 않고 GET 응답의 `clientSecretConfigured`, `apiKeyConfigured`로 설정 여부만 확인합니다. 두 필드는 조회 전용이므로 PUT 입력에 포함하지 않습니다. OIDC는 `redirectUrl`, `roleClaim`, `roleMappings`, `allowInsecureHttp`, AI는 `allowInsecureHttp`, 미디어는 `maxUploadBytes`, `maxPerPost`를 관리 화면에서 편집합니다.

Streamable HTTP MCP의 서비스 endpoint는 `/mcp`이며 개인 키의 `mcp:use` 권한을 사용합니다.
