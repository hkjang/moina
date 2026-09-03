# MOINA 오프라인 운영 가이드

## 책임 경계

GitHub Release에는 `linux/amd64`용 `moina:v0.1.20` 서비스 이미지 하나를 저장한 `moina-v0.1.20.tar.gz`만 포함됩니다. PostgreSQL, reverse proxy, DNS, 인증서, backup 저장소는 운영기관이 제공합니다.

## 반입과 설치

1. 연결 구역에서 릴리스 파일과 본문 SHA256을 서로 다른 경로로 확보합니다.
2. 반입 구역에서 해시와 gzip을 확인합니다.
3. 이미지를 load하고 tag, platform, non-root와 OCI label을 검사합니다.
4. 외부 PostgreSQL을 준비하고 네 환경변수만 설정합니다.
5. `pull never`, read-only, dropped capabilities로 시작합니다.

```bash
sha256sum moina-v0.1.20.tar.gz
gzip -t moina-v0.1.20.tar.gz
gzip -dc moina-v0.1.20.tar.gz | docker image load
docker image inspect moina:v0.1.20
cp .env.example .env
chmod 600 .env
docker compose --env-file .env -f deploy/docker-compose.offline.yml up -d --pull never
```

기본 bind는 `127.0.0.1:8080`입니다. 기관 TLS reverse proxy가 원래 `Host`, HTTPS protocol, SSE flush와 WebSocket upgrade를 전달해야 합니다. 같은 Origin의 최신 브라우저 요청은 Fetch Metadata로 최초 로컬 관리자 로그인까지 안전하게 bootstrap할 수 있습니다. 로그인 직후 **서비스 관리자 → 일반 설정 → Reverse Proxy 신뢰 정책**에 MOINA로 직접 연결하는 Proxy IP 또는 CIDR만 등록하고 다시 로그인한 뒤 OIDC를 설정합니다. 등록되지 않은 Peer의 `Forwarded`·`X-Forwarded-*` 헤더는 Client IP·OIDC redirect 계산에 사용하지 않습니다.

SPA 문서와 manifest는 `Cache-Control: no-cache`, Vite hash asset은 1년 `immutable`로 제공됩니다. 존재하지 않는 이전 릴리스 chunk는 HTML fallback 대신 `404 asset_not_found`와 `no-store`를 반환합니다. Origin 검사는 자격 증명을 처리하는 `/api/v1/**`와 `/mcp` 경계에 적용하며 정적 파일에는 적용하지 않습니다.

## 상태와 관측

| Endpoint | 의미 |
| --- | --- |
| `/healthz` | 프로세스가 요청을 받을 수 있음 |
| `/readyz` | PostgreSQL migration과 필수 의존성이 준비됨 |
| `/api/v1/version` | service name, version과 프로세스 시작 시각 |
| `/metrics` | Prometheus text 형식 운영 지표 |

```bash
curl --fail http://127.0.0.1:8080/healthz
curl --fail http://127.0.0.1:8080/readyz
curl --fail http://127.0.0.1:8080/api/v1/version
curl --fail http://127.0.0.1:8080/metrics
docker compose -f deploy/docker-compose.offline.yml ps
docker compose -f deploy/docker-compose.offline.yml logs --since 15m moina
```

`/metrics`에는 Flow·검색 지연, Flow 요청당 SQL 수, 전체 SQL 수, Outbox 지연·실패, PostgreSQL pool과 현재 WebSocket 연결 수가 포함됩니다. 사용자·본문·SQL문·인자 같은 고카디널리티 또는 민감 label은 만들지 않습니다. endpoint 자체에는 공개 인터넷용 인증이 없으므로 loopback/운영망에서만 수집하고 reverse proxy에서 접근 대역을 제한합니다.

모든 HTTP 응답의 `X-Request-ID`와 JSON 구조화 access log의 `request_id`를 함께 사용하면 API 요청을 연결해 추적할 수 있습니다. 외부에서 전달한 ID는 안전한 문자와 길이를 만족할 때만 사용합니다. 로그에 DSN, password, session cookie, CSRF token, OIDC/AI secret, 개인 key 원문과 Moin 비공개 본문을 남기지 않습니다. 오류 code·발생 시각과 관리자 화면의 감사 event ID를 함께 사용합니다.

## Migration과 검색 준비

`v0.1.4`은 시작 시 `pg_trgm` 확장을 만들고 trigram·전문 검색 index를 생성합니다. migration 계정이 extension 생성 권한이 없다면 DBA가 대상 database에 `CREATE EXTENSION IF NOT EXISTS pg_trgm`을 먼저 실행해야 합니다. 별도 OpenSearch나 인터넷 연결은 필요하지 않습니다.

적용한 migration에는 SHA-256 checksum을 저장합니다. 이미 기록된 SQL 파일이 바뀌어 checksum이 다르면 시작을 중단하므로, 운영 DB의 `schema_migrations`를 임의 수정하거나 적용 완료 migration 파일을 덮어쓰지 않습니다. **DB에 현재 binary가 알지 못하는 migration version이 하나라도 있으면 downgrade로 판단해 기동을 거부합니다.** 새 변경은 항상 다음 번호 migration으로 배포합니다. v0.1.4의 migration 005는 Moin 관계별 `post_media.alt_text`와 기존 기본 설명 backfill을, 006은 다중 인스턴스 공용 `rate_limit_buckets`를, 007은 사용자별 `notification_digest_state`를 추가합니다. Migration 008은 알림 전달 row에 `notifications.in_app`을 추가하고 In App 미확인 조회용 부분 index를 만들며, 009는 Outbox 실제 전달 시각인 `notifications.delivered_at`과 Digest 집계 index를 추가합니다. Migration 010은 각 일반 알림의 Digest 처리 완료 시각인 `notifications.digested_at`과 미처리 알림 index를 추가해 worker 실행 사이에 commit된 전달 row도 다음 Digest가 이어서 처리하도록 합니다. Migration 011은 `notification_digest_state.config_signature`에 끔·시간별·일별+시각 구성을 기록합니다. 기존 상태에는 현재 저장 설정의 signature를 backfill해 업그레이드 직후 이미 도래한 정상 window를 임의로 버리지 않습니다. Migration 012는 SMTP로 실제 전달한 알림에 `notifications.emailed_at`을 기록합니다. Migration 013은 보존 정리가 순차 스캔 대신 index 범위 스캔으로 동작하도록 `notifications(created_at)`과 전달 완료 `outbox_events(delivered_at)` index를 추가합니다. Migration에는 연결 단계와 분리된 최대 30분 제한이 적용되며 완료되기 전에는 readiness가 성공하지 않습니다. 대용량 index 생성 중 배포 관리자가 컨테이너를 조기 종료하지 않도록 startup/readiness 허용 시간을 30분 이상으로 잡고, 한도 초과 시 log와 PostgreSQL lock·I/O·권한을 확인합니다.

Flow cursor는 서명 없는 versioned Base64 URL opaque 데이터이지만 서버가 내부 값을 검증합니다. Following은 게시 시각·ID를, For Me는 기준 시각·점수·ID·랭킹 버전과 사용자별 server ranking snapshot ID를 포함합니다. For Me는 필터를 통과한 최근 후보 최대 200개의 합계·component 점수와 당시 개인화 설정을 고정합니다. 동일 사용자·랭킹 버전·설정은 같은 30초 bucket에서 snapshot을 재사용합니다.

Snapshot의 시간 만료는 생성 후 한 시간이지만 사용자당 활성 값은 최대 3개입니다. 반복 refresh로 새 snapshot을 계속 만들면 오래된 값이 한 시간 전에 제거될 수 있습니다. `ranking_version_mismatch` 또는 `feed_snapshot_expired`를 받은 client는 저장한 cursor를 버리고 첫 페이지부터 다시 조회합니다. 생성 시 사용자별 오래된/만료 snapshot을, background cleanup은 시간 만료 snapshot을 정리하므로 `feed_snapshots`와 `feed_snapshot_items`의 증가량도 DB 용량 지표로 관찰합니다.

동일 사용자의 For Me 첫 페이지 생성이 겹치면 서버는 대기열을 쌓는 대신 `feed_snapshot_busy`와 HTTP `429`, `Retry-After: 1`을 반환합니다. Browser는 1초 뒤 첫 페이지를 다시 요청합니다. 이 오류가 지속되면 같은 계정의 반복 refresh·자동화 client를 중지하고 Flow latency와 PostgreSQL lock을 확인합니다.

## Outbox와 실패 이벤트

게시·Signal·Link·승인과 알림 이벤트는 업무 데이터와 같은 PostgreSQL transaction에서 Outbox에 저장됩니다. 내장 worker는 `FOR UPDATE SKIP LOCKED`로 여러 인스턴스 사이에 일을 나누고, `LISTEN/NOTIFY`로 즉시 깨우며 polling을 복구 경로로 유지합니다. 실패는 지수 백오프로 재시도되고 한도를 넘으면 Dead Letter가 됩니다.

`서비스 관리자 → 감사 로그 → 실패 이벤트 복구`에서 마지막 오류와 시도 횟수를 확인하고 원인을 먼저 해결한 뒤 **재처리**합니다. 목록 조회는 `admin:access`와 `audit:read`, 상태를 바꾸는 재처리는 `admin:access`와 `outbox:manage`가 필요합니다. 조사 전용 역할과 복구 역할을 분리하고, 재처리 동작이 감사 로그에 남는지 확인합니다. 대기 지연은 `moina_outbox_lag_seconds`, 처리 실패 누계는 `moina_outbox_failures_total`로 경보를 구성합니다.

알림은 commit된 PostgreSQL row가 source of truth이고 `LISTEN/NOTIFY`는 실시간 fanout hint입니다. Listener의 bounded channel이 가득 차면 signal을 버리지 않고 consumer가 따라올 때까지 backpressure를 적용합니다. Browser별 queue가 가득 찬 느린 WebSocket은 연결을 종료하고 client가 최대 30초 지수 backoff로 재연결합니다. 재연결 직후와 연결 중 매 60초마다 REST unread summary를 다시 읽으므로 일시적인 LISTEN·socket 공백은 지속적인 알림 유실이 되지 않습니다. In App을 끈 유형도 cross-instance Toast/Desktop 전달을 위해 `in_app=false`와 읽음 상태의 durable row로 남기고 알림 목록·미확인 수에서만 제외합니다. 사용자별 Toast·Desktop·조용한 시간과 Digest 설정은 WebSocket의 독립 `inApp`·`toast`·`desktop` flag에 적용되며 승인·보안 알림은 In App 운영 기록으로 항상 노출합니다. Digest는 업무 event 발생 시각이 아니라 Outbox가 알림 row를 실제 저장한 `delivered_at` 순서로 아직 `digested_at`이 없는 전달 row를 집계하고, 처리한 row를 같은 transaction에서 표시합니다. 따라서 지연 처리되거나 집계 도중 commit된 알림도 다음 실행에서 누락되지 않습니다. Digest를 새로 켜거나 mode·일별 시각을 바꾸면 현재 시각을 새 기준선으로 잡아 꺼져 있던 기간이나 이전 일정의 backlog를 재생하지 않습니다. 여러 인스턴스의 worker는 PostgreSQL advisory lock과 상태 테이블로 중복 생성을 막으며, 손상된 사용자 설정 한 건은 savepoint 단위로 격리해 다른 사용자의 Digest를 중단시키지 않습니다.

### SMTP 알림 전달

관리자는 **SMTP 메일 설정**에서 저장 후 테스트 메일을 현재 관리자 프로필 이메일로 전송해 DNS, TLS, 인증, 발신·수신 정책을 확인합니다. 사용자 이메일 알림은 별도의 `notification.email` Outbox 이벤트로 전달되므로 SMTP 장애가 게시·Signal·Link transaction을 되돌리지 않습니다. 실패 이벤트의 `lastError`로 DNS 정책, TLS 인증서, STARTTLS 지원, 인증 또는 relay 거부를 구분하고 원인을 해결한 뒤 재처리합니다. 성공한 알림은 `emailed_at`으로 표시합니다. Outbox는 at-least-once 전달이므로 SMTP 전송 직후 DB marker 기록 전에 process가 중단되는 극히 짧은 구간에는 같은 메일이 재시도될 수 있습니다.

| SMTP 증상 | 우선 확인 |
| --- | --- |
| 사설망 주소 거부 | host에 port가 섞이지 않았는지 → 사설망 SMTP 허용 → 컨테이너 DNS가 정확한 RFC1918/ULA를 반환하는지 |
| STARTTLS 실패 | server가 EHLO에 STARTTLS를 광고하는지 → CA bundle → hostname과 인증서 SAN |
| 인증·relay 거부 | 암호화 연결 여부 → username/password → 보내는 주소와 받는 domain relay 정책 |
| 메일은 실패하지만 Moin은 게시됨 | 정상 분리 동작. 감사 로그의 실패 이벤트 복구에서 `notification.email` 확인 |

## Client IP와 전역 요청 한도

감사 기록의 `socketIp`는 직접 연결 Peer, `clientIp`는 신뢰 Proxy chain을 오른쪽부터 검증해 계산한 사용자 주소이며 `proxyChain`은 검증에 사용한 전체 주소 목록입니다. 신뢰 Proxy를 등록하지 않은 상태에서 임의 `X-Forwarded-For`를 보내도 `clientIp`는 소켓 주소로 유지되어야 합니다. `Forwarded: proto=`와 `X-Forwarded-Proto`도 가장 가까운 오른쪽 hop의 값만 사용합니다. 설정 변경 뒤 별도 재시작은 필요하지 않습니다.

로그인·가입과 개인 API/MCP key의 요청 한도는 PostgreSQL `rate_limit_buckets`에서 원자적으로 계산하므로 인스턴스를 늘려도 quota가 배수로 증가하지 않습니다. 만료 bucket은 background worker가 정리합니다. PostgreSQL 오류로 전역 한도를 확인할 수 없을 때는 fail-open하지 않고 HTTP 503 `rate_limit_unavailable`을 반환합니다.

## 미디어 업로드 계약 확인

인증된 작성 client는 업로드 전에 `GET /api/v1/media/config`로 현재 제한을 확인할 수 있습니다. 응답은 `maxUploadBytes`, `maxPerPost`, `acceptedTypes`만 제공하며 `orphanTtlHours`는 관리자 설정에만 남습니다. 이 endpoint는 `posts:write` 권한이 필요합니다. 별도로 사용자 한 명이 보유할 수 있는 미첨부 media는 최대 100개·512 MiB이며 이 quota는 `v0.1.20` 관리자 설정이 아닙니다.

```bash
curl --fail http://127.0.0.1:8080/api/v1/media/config \
  -H 'Authorization: Bearer mk_REDACTED'
```

관리자가 업로드 제한을 바꾼 직후에는 작성 화면의 이전 값과 서버 값이 잠시 다를 수 있습니다. HTTP `413`, `415` 또는 설정 오류를 받으면 client가 설정을 다시 조회하도록 안내하고, 서버 검증을 우회하지 않습니다. HTTP `429`와 `media_quota_exceeded`는 기존 업로드를 Moin·프로필에 연결하거나 orphan TTL 정리를 기다린 뒤 재시도합니다.

Large Object 다운로드는 인스턴스당 최대 8개를 동시에 열고, PostgreSQL pool이 작으면 일반 API용 연결 5개를 남기도록 media read slot을 줄입니다. 느린 다운로드가 slot을 오래 점유하면 새 read가 요청 context 안에서 대기하므로 reverse proxy timeout과 DB pool 사용량을 함께 봅니다. Cleaner는 매시간 500개씩 최대 20 batch, 즉 인스턴스당 한 주기에 최대 10,000개를 정리합니다. 여러 인스턴스는 `SKIP LOCKED`로 대상 충돌을 피합니다.

## 보존 정리

시작 직후와 이후 매시간, 한 인스턴스가 advisory lock을 잡고 만료 session과 보존 기간이 지난 운영 레코드를 정리합니다. 한 주기에 테이블당 최대 5,000행씩 20회까지만 삭제하므로 오래 정리하지 않은 설치에서도 한 번에 긴 lock을 잡지 않고, 남은 행은 다음 주기가 이어서 처리합니다.

기본값은 알림 90일, 전달 완료 Outbox event 14일, AI 사용 기록 180일이며 감사 기록은 무기한입니다. 값은 `service.retention` 설정으로 바꿉니다(`docs/configuration.md` 참고). 정리한 행 수는 `보존 기간 초과 레코드 정리` log로 남습니다.

업그레이드 직후 첫 주기에서 이전 릴리스가 삭제할 수 없던 만료 session과 오래된 알림이 한 번에 정리되므로, `audit_events`·`notifications`·`outbox_events` 크기와 PostgreSQL I/O를 함께 관찰합니다. 삭제된 공간의 실제 반환은 autovacuum 일정에 따릅니다.

## PostgreSQL backup과 복구

운영 RPO/RTO에 맞는 물리 또는 논리 backup을 사용합니다. 최소 보관 집합은 다음과 같습니다.

- PostgreSQL backup과 WAL(사용 시)
- backup 시점의 `MOINA_ENCRYPTION_KEY`
- 배포한 `moina-vX.Y.Z.tar.gz`와 SHA256
- `schema_migrations`와 `outbox_attempts`를 포함한 schema/version·재시도 정보
- PostgreSQL Large Object를 포함한 전체 database dump

새 미디어는 PostgreSQL Large Object로 streaming 저장됩니다. 논리 backup은 blob/Large Object가 포함되는 전체 database dump(`pg_dump -b` 또는 사용 중인 PostgreSQL 버전의 동등 옵션)를 사용합니다. 복구 훈련은 격리된 PostgreSQL에 복원한 뒤 같은 encryption key로 로그인, 미디어 조회, OIDC/AI secret 복호화 여부, Moin/Link/키 목록과 audit·Outbox 연속성을 확인합니다.

## 업그레이드

1. release notes와 migration의 forward/backward 호환성을 검토합니다.
2. DB와 encryption key를 backup하고, 서비스 중단 전에 기존 로컬 bootstrap 최고 관리자 로그인이 되는지 확인합니다.
3. 새 tar.gz를 검증하고 load합니다.
4. staging에서 최대 30분 migration 시간, 로그인과 Playwright smoke를 검증합니다.
5. compose image tag를 바꾸고 서비스를 재시작합니다.
6. readiness, 버전, 로그인, Flow, 검색, 알림, 관리자 설정을 확인합니다.

### v0.1.20 업그레이드 시 확인

Migration 013이 `notifications`와 `outbox_events`에 index를 추가하므로 두 테이블이 큰 설치에서는 기동 시간이 늘어날 수 있습니다. 기동 직후 첫 보존 정리가 실행되며, 기본값으로도 90일 넘은 알림과 14일 넘은 전달 완료 Outbox event가 삭제됩니다. 이 기록을 더 오래 보관해야 한다면 업그레이드 **전에** `service.retention`을 원하는 값으로 먼저 설정합니다. 감사 기록은 기본값이 무기한이므로 별도 조치가 필요 없습니다.

### v0.1.0 내부 OIDC·AI 사용자의 필수 조치

`v0.1.4`은 사설 주소를 자동 허용하지 않습니다. `v0.1.0`에서 RFC1918/ULA로 해석되는 Keycloak/OIDC 또는 AI endpoint를 사용했다면 업그레이드 뒤 해당 연결은 `privateAllowedHosts`를 명시적으로 저장하기 전까지 실패합니다. 기존 host를 자동으로 사설 예외로 승격하지 않는 것은 SSRF 방어를 약화하지 않기 위한 의도된 변경입니다.

1. OIDC에 의존하지 말고 검증해 둔 **로컬 bootstrap 최고 관리자**로 로그인합니다. Bootstrap 환경변수의 비밀번호를 바꿔도 이미 생성된 계정 비밀번호는 재설정되지 않습니다.
2. Keycloak/OIDC와 AI 설정 각각의 `allowedHosts`에 endpoint의 정확한 authority가 있는지 확인합니다.
3. 사설 주소를 반환하는 정확한 DNS hostname을 `privateAllowedHosts`에도 저장합니다. 비기본 port는 두 목록 모두 같은 `host:port`로 입력하고 IP literal은 사용하지 않습니다.
4. 연결 테스트를 통과한 뒤 OIDC 로그인과 AI streaming을 확인합니다.

공인 IP만 반환하는 endpoint에는 `privateAllowedHosts`가 필요하지 않습니다. 로컬 관리자 credential을 확인하지 못했다면 OIDC 전용 운영 중단 위험이 있으므로 업그레이드를 시작하지 말고 먼저 복구 절차를 승인합니다.

새 schema를 이전 binary가 읽지 못하면 앱 image만 되돌리는 rollback은 안전하지 않습니다. migration별 rollback 계획을 별도로 승인합니다.

## 키 운영

- root encryption key는 application 관리자 계정과 분리해 vault/HSM 수준으로 보관합니다.
- 개인 API/MCP key는 사용자 화면에서 회전하고 이전 token은 즉시 폐기합니다.
- `v0.1.20`는 root key online rotation을 제공하지 않습니다. 값을 바꾸면 저장 비밀과 기존 session/API key verifier를 사용할 수 없으므로 원본을 보관하고 임의 교체하지 않습니다.
- 유출이 의심되면 관련 key 폐기, session 종료, audit 조사와 downstream secret rotation을 함께 수행합니다.

## 장애 분류

| 현상 | 확인 순서 |
| --- | --- |
| 컨테이너 반복 종료 | 필수 env 형식 → PostgreSQL DNS/TLS → migration log |
| readiness만 실패 | 최대 30분 migration 진행 여부 → DB pool/권한/용량과 migration lock |
| migration checksum 불일치 | 적용 SQL 임의 변경 여부 확인 → 원본 release migration 복원 |
| binary보다 새로운 migration 감지 | 잘못된 image downgrade 여부 확인 → DB schema와 일치하는 정식 release image로 복구 |
| Flow 새로고침 429 | `feed_snapshot_busy` 확인 → `Retry-After: 1` 준수 → 같은 계정의 반복 refresh/자동화 중지 |
| 로그인·가입·API key 요청 503 | `rate_limit_unavailable` 확인 → PostgreSQL 연결·lock·용량과 `rate_limit_buckets` migration 확인 |
| JS asset 403·빈 화면 | 응답의 `invalid_origin` 확인 → v0.1.4 이상 배포 → proxy의 원래 Host 전달 → index `no-cache`와 asset 200/immutable 확인 |
| 감사 Client IP 이상 | 직접 Peer가 신뢰 Proxy IP/CIDR인지 확인 → `socketIp`·`clientIp`·`proxyChain` 비교 → 임의 Forwarded header 차단 확인 |
| OIDC 실패 | 연결 테스트의 정확한 대상·DNS 결과 확인 → `oidc_private_host_required`는 자동 입력 → `oidc_address_forbidden`은 DNS/endpoint 변경 → Client Secret·Standard flow·PKCE·redirect URI·clock skew 확인 |
| AI streaming 지연 | endpoint → `allowedHosts`/`privateAllowedHosts`와 port → DNS/CA → reverse proxy buffering/timeout |
| WebSocket 끊김 | upgrade header → idle timeout → origin 정책 → slow-client 재연결과 60초 REST reconcile |
| Outbox 지연·Dead Letter 증가 | `/metrics` → 관리자 실패 이벤트 → PostgreSQL lock/오류 → 원인 해결 뒤 재처리 |
| 미디어 업로드 거부 | 오류 code 확인 → `media_quota_exceeded`면 미첨부 100개/512 MiB → MIME/파일 byte·개수 → 설정 cache |
| 미디어 용량 증가 | orphan TTL 설정 → 인스턴스당 시간당 최대 10,000개 drain log → Moin/프로필 참조와 Large Object backup 확인 |
| 복호화 실패 | 올바른 root key와 backup 시점 확인, 쓰기 작업 중단 |

## 제거

```bash
docker compose --env-file .env -f deploy/docker-compose.offline.yml down
```

이 명령은 외부 PostgreSQL 데이터를 삭제하지 않습니다. image와 DB를 영구 제거하려면 별도 변경 승인과 backup 보존 정책을 따릅니다.
