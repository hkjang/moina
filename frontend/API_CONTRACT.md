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
| GET | `/users/{username}`, `/topics`, `/topics/{slug}`, `/search`, `/notifications`, `/notifications/email/status`, `/moims`, `/moims/{slug}` |
| POST, DELETE | `/links/{userId}`, `/topics/{slug}/follow`, `/moims/{slug}/members` |
| POST | `/notifications/read`, `/moims`, `/media`, `/reports` |
| GET | `/media/config`, `/media/{id}` |
| DELETE | `/media/{id}` (게시물·avatar에 연결되지 않은 본인 업로드 정리) |
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

게시물 작성 body는 `content`, `visibility`, `mediaIds`와 선택적인 `replyToId`, `quoteMoinId`를 사용합니다. 수정 PATCH는 `content`와 선택적인 `mediaIds`, `mediaAltTexts`를 사용합니다. `mediaIds` 생략은 기존 첨부 유지, 빈 배열은 전체 제거, 배열 제공은 표시 순서를 포함한 전체 교체이며 본인이 업로드한 media만 허용합니다. 개인 프로필 이미지는 `/media`에 올린 본인 이미지 ID를 `PATCH /profile`의 `avatarId`로 저장하고 빈 문자열로 제거합니다. 브라우저 세션은 프로필 media를 관리할 수 있고 API key에는 기존 `posts:read`·`posts:write` 범위가 필요합니다. `/notifications/email/status`는 SMTP 세부 값 대신 `available`, `smtpConfigured`, `recipientConfigured`로 현재 사용자의 메일 수신 준비 상태만 제공합니다. AI 응답은 기본 SSE 스트리밍이며 `maxTokens`는 관리자가 정한 범위에서 최대 262,144입니다. WebSocket의 최초 `{type:"connected"}` 이벤트는 알림으로 표시하지 않습니다.

`@username`은 작성·수정 모두 공개 범위와 차단 관계를 확인한 뒤 post 단위 멱등 멘션 알림을 만듭니다. 작성 UI는 `/search?type=users` 자동완성 결과를 키보드나 포인터로 삽입하며, 피드의 멘션과 해시태그는 각각 프로필·토픽 링크로 렌더링합니다.

## 서비스 관리자

| Method | Path |
| --- | --- |
| GET | `/admin/stats`, `/admin/users`, `/admin/posts`, `/admin/reports`, `/admin/roles`, `/admin/audit` |
| POST | `/admin/users` |
| PATCH | `/admin/users/{id}`, `/admin/posts/{id}`, `/admin/reports/{id}` |
| GET, PUT | `/admin/oidc`, `/admin/ai`, `/admin/smtp`, `/admin/workflow` |
| POST | `/admin/oidc/test`, `/admin/ai/test`, `/admin/smtp/test` |
| GET | `/admin/settings` |
| PUT | `/admin/settings/{key}` |
| PUT | `/admin/roles` |

일반 설정 key는 `service.general`, `api.access`, `media.config`입니다. OIDC, AI와 SMTP 비밀 값은 조회하지 않고 GET 응답의 `clientSecretConfigured`, `apiKeyConfigured`, `passwordConfigured`로 설정 여부만 확인합니다. 이 필드는 조회 전용이므로 PUT 입력에 포함하지 않습니다. SMTP는 전용 저장·테스트 API를 사용하고 사용자 `notifications.email.enabled`가 켜진 알림을 독립 Outbox로 전달합니다. OIDC는 `redirectUrl`, `roleClaim`, `roleMappings`, `allowInsecureHttp`, AI는 `allowInsecureHttp`, 미디어는 `maxUploadBytes`, `maxPerPost`를 관리 화면에서 편집합니다.

Streamable HTTP MCP의 서비스 endpoint는 `/mcp`이며 개인 키의 `mcp:use` 권한을 사용합니다.
