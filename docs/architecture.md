# MOINA 아키텍처

## 선택

MOINA `v0.1.0`은 Go modular monolith와 React SPA를 단일 binary/image로 배포합니다. 초기 제품에서 microservice 운영 복잡도를 만들지 않으면서 모듈 경계를 유지하고, 실제 부하가 확인되면 독립 worker나 search/notification service로 분리할 수 있게 합니다.

```text
Browser (React, REST/SSE/WebSocket)
                 │
        Go HTTP application
 ┌───────────────┼────────────────┐
 auth  users  posts  social  feed
 topics  moims  search  notify  ai
 keys  mcp  moderation  approval
 settings  audit
 └───────────────┼────────────────┘
             PostgreSQL
```

React 정적 asset은 Go binary가 같은 origin에서 제공합니다. 외부 font/CDN이 없으므로 CSP와 폐쇄망 배포가 단순합니다.

## 데이터 경계

핵심 관계:

```text
User ─writes→ Post(Moin) ─replies→ Post
User ─links→ User / Topic / Moim
Post ─tags→ Topic
Post ─belongs→ Moim
User ─reacts/saves→ Post
User ─owns→ APIKey / Preference / Session
Report ─targets→ User / Post / Moim
Approval ─guards→ configured action snapshot
```

ID는 CSPRNG로 만든 prefix 포함 opaque 문자열을 사용합니다. collection은 `limit`과 숫자 `offset`을 사용하며 일부 응답의 `nextCursor`도 다음 offset을 문자열로 표현합니다. timestamp는 PostgreSQL `timestamptz`와 UTC 시각을 사용하고 UI는 브라우저 locale로 표시합니다.

## Feed

초기 Following Flow는 PostgreSQL query로 fan-out on read를 수행합니다. For Me는 최근 후보 최대 200개를 가져와 사용자 설정으로 점수를 계산합니다.

```text
후보: 공개 범위가 허용된 최근 Moin
  → permission/차단/삭제 filter
  → Link·팔로우 Topic·Signal·최신성 score
  → 제외 Topic filter
  → item + Why this Moin 설명
```

점수와 화면의 추천 이유는 같은 Moin 관계·Topic·Signal 정보를 사용합니다. 사용자가 설정한 네 가중치와 제외 Topic, 이유 표시 여부는 개인 preference JSON에 저장됩니다. 규모가 커지면 별도 timeline worker와 hybrid fan-out을 도입할 수 있습니다.

## 실시간과 비동기

WebSocket은 새 알림을 연결된 브라우저로 전달하고 PostgreSQL이 source of truth입니다. 연결이 끊기면 REST 알림 목록을 `limit`/`offset`으로 다시 읽습니다. `v0.1.0`은 별도 message broker나 outbox worker를 실행하지 않습니다.

## 검색

`v0.1.0` 검색은 PostgreSQL의 case-insensitive `LIKE` 조건으로 사용자, Moin, Topic과 Moim을 찾으며 별도 OpenSearch나 확장을 요구하지 않습니다. 대규모 데이터에서는 full-text/trigram 또는 전용 검색 index를 후속 도입해야 합니다.

## 인증과 설정

로컬 session과 OIDC가 같은 내부 user/role 모델로 수렴합니다. 환경변수는 DB 연결, bootstrap 관리자와 root encryption key 네 개뿐입니다. OIDC와 AI의 비밀 설정은 암호화하고, 일반·승인·permission 설정은 PostgreSQL revision과 audit log로 관리합니다.

## AI와 MCP

AI adapter는 OpenAI-compatible Responses/Chat Completions 요청 차이를 흡수하고 upstream SSE를 그대로 streaming합니다. HTTPS URL을 기본으로 검증하지만 `v0.1.0`에는 DNS/IP 재검증과 redirect 제한이 없으므로 운영망의 outbound allowlist가 필요합니다. API key는 요청 시에만 복호화합니다.

REST, UI와 MCP는 별도 business logic을 복제하지 않고 동일 service method와 authorization policy를 호출합니다. 승인 대상 action은 어느 진입점에서도 같은 snapshot/검토 규칙을 적용합니다.

## 오프라인 runtime

```text
moina:v0.1.0 (linux/amd64, distroless, non-root, read-only)
  ├─ /app/moina
  └─ /app/web/dist

외부 연결: PostgreSQL(필수), 내부 Keycloak/AI(선택)
인터넷 연결: 없음
```

Migration은 binary에 embed하고 시작 시 PostgreSQL advisory lock 아래 순서대로 적용합니다. readiness는 migration과 DB ping이 끝난 뒤에만 성공합니다.

## 확장 경계

다음은 트래픽이나 제품 검증 뒤 분리할 후보입니다.

- notification/outbox worker
- media object storage와 image/video processor
- dedicated search/embedding index
- recommendation batch/online ranker
- ActivityPub federation gateway

분리는 관측된 병목과 독립 확장 필요가 있을 때 수행합니다. core API와 event schema를 먼저 versioning합니다.
