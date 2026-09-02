# MOINA 아키텍처

## 선택

MOINA `v0.1.13`은 Go modular monolith와 React SPA를 단일 binary/image로 배포합니다. 초기 제품에서 microservice 운영 복잡도를 만들지 않으면서 모듈 경계를 유지하고, 실제 부하가 확인되면 독립 worker나 search/notification service로 분리할 수 있게 합니다.

```text
Browser (React, REST/SSE/WebSocket)
                 │
        Go HTTP application
 ┌───────────────┼────────────────┐
 auth  users  posts  social  feed
 topics  moims  search  notify  ai
 keys  mcp  moderation  approval
 outbox worker  media  metrics
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

ID는 CSPRNG로 만든 prefix 포함 opaque 문자열을 사용합니다. 일반 관리 collection은 `limit`과 숫자 `offset`을 유지하지만 Flow는 버전형 Base64 URL 키셋 `cursor`를 사용합니다. Following은 `(published_at, id)`, For Me는 `(asOf, score, id, rankingVersion, snapshotID)`를 보존합니다. timestamp는 PostgreSQL `timestamptz`와 UTC 시각을 사용하고 UI는 브라우저 locale로 표시합니다.

## Feed

Following Flow는 PostgreSQL query로 fan-out on read를 수행합니다. For Me는 `asOf` 시점에 공개 범위와 제외 Topic 정책을 통과한 Moin을 최신 게시 순으로 최대 200개 선택한 뒤 설명 가능한 점수를 계산합니다. 첫 페이지는 사용자·`rankingVersion`·개인화 설정 hash에 결합된 server ranking snapshot을 만들고, 후보 ID·합계 점수·Link/Topic/발견/최신성 component·팔로우 Topic 수와 당시 preference JSON을 PostgreSQL에 고정합니다.

```text
후보: asOf 시점에 공개 범위가 허용된 Moin
  → permission/차단/삭제/제외 Topic filter
  → published_at 최신순 최대 200개
  → Link·팔로우 Topic·Signal·최신성 score와 component
  → preference·score component ranking snapshot
  → (score, id) keyset
  → item + Why this Moin 설명
```

점수와 화면의 추천 이유는 snapshot에 저장된 같은 component를 사용하므로 스크롤 중 Signal·Link·개인화 설정이 바뀌어도 이미 열린 Flow의 정렬과 설명이 섞이지 않습니다. 동일 사용자·랭킹 버전·설정은 30초 bucket 안에서 같은 snapshot을 재사용합니다. 사용자별 활성 snapshot은 최대 3개이고 생성 시 오래된 값을 제거합니다. 각 snapshot의 시간 만료는 생성 기준 한 시간이지만 반복 refresh로 3개를 넘기면 이전 cursor가 더 일찍 `feed_snapshot_expired`가 될 수 있습니다.

Snapshot 생성은 사용자 단위 `pg_try_advisory_xact_lock`으로 직렬화합니다. 같은 사용자의 첫 페이지 생성이 겹치면 DB connection을 잠금 대기로 점유하지 않고 `feed_snapshot_busy`, HTTP 429와 `Retry-After: 1`로 빠르게 반환합니다.

이후 페이지는 cursor의 `snapshotID`가 현재 사용자와 랭킹 버전에 속하고 남아 있는지 확인한 뒤 고정된 `(score, id)`를 읽습니다. 랭킹 공식이 바뀌면 `rankingVersion`이 다른 cursor도 거부합니다. 두 오류 모두 client가 첫 페이지부터 다시 조회해야 합니다. Cursor는 opaque 운반 형식이지 인증 credential이 아니며 server-side snapshot binding이 다른 사용자의 재사용을 막습니다.

각 Flow 페이지는 root 후보, 보이는 Quote/Remoin, 미디어·Topic·Signal·카운트·viewer 상태를 일괄 조회하는 **고정 3회 SQL**로 hydration합니다. 반환 Moin 수에 따라 query 수가 증가하지 않으며 관련 Moin도 같은 batch에 포함합니다. 프런트엔드는 cursor별 페이지 Map과 Moin ID 병합을 사용하고 Signal·Pocket·Remoin을 optimistic update해 전체 Flow를 다시 읽지 않습니다.

## 실시간과 비동기

게시·Signal·Link·승인과 알림 이벤트는 업무 데이터와 같은 transaction에서 `outbox_events`에 기록합니다. 단일 Go binary 안의 worker가 `FOR UPDATE SKIP LOCKED`로 claim하고 지수 백오프, idempotency key와 Dead Letter를 적용합니다. PostgreSQL `LISTEN/NOTIFY`는 여러 인스턴스를 즉시 깨우고 polling은 연결 장애 시 복구 경로가 됩니다. 사용자 이메일 채널이 켜진 알림은 notification row와 같은 transaction에서 별도 `notification.email` 이벤트를 생성해 SMTP 장애가 원래 업무 transaction이나 인앱 전달을 막지 않게 합니다.

SMTP worker는 전달 시점의 사용자 수신 설정, 활성 계정과 관리자 SMTP 설정을 다시 확인합니다. 일반 활동은 Digest 정책에 따라 즉시 메일 대신 요약 알림으로 모으고 멘션·승인·보안은 즉시 전달합니다. SMTP password가 포함된 설정 document는 AEAD로 암호화하고, 정확한 `host:port`의 DNS 결과를 연결 직전에 재검증합니다. 성공 후 `notifications.emailed_at`을 기록하며 실패는 독립 Outbox 재시도·Dead Letter로 이동합니다.

WebSocket은 새 알림을 연결된 브라우저로 전달하고 PostgreSQL이 source of truth입니다. 각 인스턴스의 LISTEN consumer는 bounded channel이 가득 차면 다음 signal 전달을 기다리는 backpressure를 적용하고, durable notification row를 읽은 뒤 자신의 Hub로 전파합니다. Browser별 queue가 가득 찬 느린 socket은 연결을 취소해 client의 지수 backoff 재연결 경로로 보냅니다. Client는 연결 시 즉시, 연결 중에도 60초마다 REST unread summary를 다시 읽어 signal·socket 공백을 보완합니다. 사용자 설정과 서비스 시간대를 적용한 `inApp`·`toast`·`desktop` flag가 각 frame에 포함되고, 조용한 시간에는 실시간 표시만 보류합니다. In App 비활성 유형도 `in_app=false`와 읽음 상태의 durable 전달 row로 저장해 다른 인스턴스의 fanout을 유지하며 REST 목록·미확인 수에서는 제외합니다. 시간별·일별 Digest worker는 PostgreSQL advisory lock과 `notification_digest_state`로 여러 인스턴스의 중복 요약을 막습니다. `notifications.delivered_at` 순서와 `digested_at IS NULL` marker로 실제 저장된 미처리 전달 row만 집계하고 처리 표시도 같은 transaction에서 갱신하므로 지연 Outbox와 worker 집계 도중 commit된 알림을 다음 실행에서 이어서 처리합니다. `config_signature`은 끔·시간별·일별+시각 전환을 감지해 새 구독 경계를 만들고 이전 일정의 backlog 재생을 막습니다. 사용자별 nested transaction은 손상된 설정을 격리해 전체 batch 진행을 보장합니다. 승인·보안 알림은 In App 운영 기록으로 항상 유지합니다. Dead Letter는 관리자 감사 화면에서 원인을 확인하고 재처리할 수 있습니다.

## 검색

`v0.1.13` 검색은 PostgreSQL `pg_trgm`, `to_tsvector('simple', ...)`와 정확 일치 가중치를 결합해 사용자, Moin, Topic과 Moim을 관련도 순으로 찾습니다. 오탈자·부분 문자열과 한국어 띄어쓰기 검색을 지원하면서 외부 OpenSearch를 요구하지 않습니다. `type`을 지정하면 해당 대상의 SQL만 실행하고, 검색 결과 Moin도 ID별 재조회 대신 일괄 hydration합니다. 공개 프로필의 Moin·Signal 통계는 공개·게시 상태의 Moin만 집계합니다. Topic Pulse는 최근 7일의 공개 Moin과 Signal을 집계하고 최근 24시간 활동에 더 큰 가중치를 적용하며, 비공개·삭제·승인 대기 Moin은 제외합니다.

## 클라이언트 빠른 이동

React shell은 `Ctrl/Command+K` 전역 팔레트와 `G` 연속 단축키를 제공합니다. 화면 catalog는 기존 navigation permission과 승인 정책을 그대로 사용하므로 접근할 수 없는 AI·관리 화면은 검색 결과와 단축키 양쪽에서 제외됩니다. 메뉴와 일치하지 않는 입력은 서버 통합 검색 URL로 전달하고, 최근 내부 경로는 사용자 ID별 local storage에 최신순 8개만 보관합니다. 임시 작성·인용 query는 기록에서 제거하고 같은 pathname의 검색 조건은 최신 하나로 합쳐 draft를 뜻하지 않게 다시 여는 동작을 막습니다. 저장값은 내부 경로 검증을 다시 거치며 서버 권한 검사를 대체하지 않습니다.

## 인증과 설정

로컬 session과 OIDC가 같은 내부 user/role 모델로 수렴합니다. 환경변수는 DB 연결, bootstrap 관리자와 root encryption key 네 개뿐입니다. OIDC와 AI의 비밀 설정은 암호화하고, 일반·승인·permission·신뢰 Proxy 설정은 PostgreSQL revision과 audit log로 관리합니다. 직접 연결 Peer가 관리자 IP/CIDR 목록에 있을 때만 Forwarded header를 신뢰하고, 오른쪽부터 신뢰 chain을 제거해 Client IP를 계산합니다. Protocol은 가장 가까운 오른쪽 hop만 사용합니다. 신뢰 Proxy를 아직 등록하지 않은 최초 로그인은 browser가 위조할 수 없는 `Sec-Fetch-Site: same-origin`과 HTTPS Origin으로 자격 증명 경계를 검증하고 Secure cookie를 설정하며, cross-site·same-site cross-origin 요청은 계속 거부합니다. 로그인·가입과 개인 API/MCP key의 bucket은 PostgreSQL에 두어 모든 인스턴스가 같은 quota를 사용합니다.

## AI와 MCP

AI adapter는 OpenAI-compatible Responses/Chat Completions 요청 차이를 흡수하고 upstream SSE를 그대로 streaming합니다. OIDC와 AI는 관리자별 exact-authority `allowedHosts`, 사설 주소 예외용 `privateAllowedHosts`, HTTPS 정책, 연결 시 DNS/IP pinning과 매 redirect 재검증을 공유합니다. port 없는 허용 항목은 scheme 기본 port에만 일치합니다. 정확한 hostname을 두 목록에 함께 등록한 경우에만 RFC1918/ULA를 허용하며 loopback·link-local·metadata·CGNAT·unspecified·multicast는 항상 거부합니다. Process proxy는 우회 경로를 만들지 않도록 비활성화합니다. API key는 요청 시에만 복호화합니다.

`v0.1.0`의 기존 설정에서 endpoint host를 알 수 있더라도 사설 주소 접근 권한은 추론하거나 자동 이관하지 않습니다. 업그레이드 후 로컬 bootstrap 최고 관리자가 `privateAllowedHosts`를 명시 저장해야 새 정책에 포함됩니다. 이는 schema migration이 egress 권한을 조용히 확대하지 않도록 하는 보안 경계입니다.

REST, UI와 MCP는 동일한 DB source of truth와 authorization policy를 사용합니다. 승인 대상 action은 전역 `*`, exact dot action 또는 terminal namespace wildcard만 허용하고 어느 진입점에서도 같은 snapshot/검토 규칙을 적용합니다. 설정한 모든 approver 역할과 알림 대상은 현재 최종 유효 `approvals:review` 권한을 다시 확인하며 요청자의 자기 승인·반려를 차단합니다.

## 미디어

HTTP 업로드·다운로드 경계는 `io.Reader`/`io.ReadCloser` 기반 `MediaStore`로 분리합니다. PostgreSQL adapter는 새 payload를 Large Object에 64 KiB copy buffer로 streaming하고 metadata·SHA-256·OID만 일반 table에 둡니다. 기존 `bytea` payload는 호환 읽기를 유지합니다. 업로드의 `media_assets.alt_text`는 기본 설명이고 Moin별 최종 대체 텍스트는 `post_media.alt_text`에 저장해 동일 media 재사용 문맥을 분리합니다. 사용자별 미첨부 media는 100개·512 MiB로 고정 제한하며 사용자 advisory lock 안에서 검사해 동시 업로드 우회를 막습니다.

Large Object read는 인스턴스당 최대 8개를 동시에 유지합니다. DB pool이 작으면 일반 API용 연결 5개를 남기도록 read slot을 더 줄이며 최소 1개는 허용합니다. 참조되지 않은 업로드는 설정형 TTL과 `SKIP LOCKED` batch 정리로 여러 인스턴스에서도 안전하게 삭제됩니다. Cleaner는 매시간 500개 batch를 최대 20회 실행해 인스턴스당 한 주기에 최대 10,000개를 drain합니다. 인증된 작성 client는 `GET /api/v1/media/config`에서 현재 `maxUploadBytes`, `maxPerPost`와 여섯 개 허용 MIME type을 읽고, 업로드 API는 같은 계약과 고정 quota를 최종 검증합니다. 관리자 전용 orphan TTL과 고정 quota는 이 응답에서 제외합니다.

## 관측

모든 요청은 검증된 `X-Request-ID`를 응답과 구조화 log에 연결합니다. 내장 `/metrics`는 Prometheus text 형식으로 Flow·검색 지연, Flow 요청당 SQL 수, 전체 SQL 수, Outbox 지연·실패, DB pool과 WebSocket 연결 수를 제공합니다. SQL문·인자·사용자 ID를 label로 수집하지 않습니다. 적용한 migration의 SHA-256 checksum은 `schema_migrations`에 저장해 과거 SQL의 사후 변경을 탐지합니다.

## 오프라인 runtime

```text
moina:v0.1.13 (linux/amd64, distroless, non-root, read-only)
  ├─ /app/moina
  └─ /app/web/dist

외부 연결: PostgreSQL(필수), 내부 Keycloak/AI(선택)
인터넷 연결: 없음
```

Migration은 binary에 embed하고 시작 시 PostgreSQL advisory lock 아래 순서대로 적용합니다. 기존 checksum을 검증하고 새 migration과 checksum을 함께 기록하며, 불일치하면 임의 변경으로 판단해 시작을 중단합니다. DB 이력에 binary가 모르는 version이 있으면 안전하지 않은 downgrade로 판단해 기동을 거부합니다. 연결 단계의 짧은 startup deadline과 분리된 최대 30분 migration context를 사용하고, 완료 또는 실패 전에는 readiness를 성공시키지 않습니다.

## 확장 경계

다음은 트래픽이나 제품 검증 뒤 분리할 후보입니다.

- media object storage와 image/video processor
- dedicated search/embedding index
- recommendation batch/online ranker
- ActivityPub federation gateway

분리는 관측된 병목과 독립 확장 필요가 있을 때 수행합니다. core API와 event schema를 먼저 versioning합니다.
