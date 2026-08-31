# MOINA 보안 모델

## 기본 가정

MOINA는 신뢰되지 않은 브라우저 사용자, 악성 콘텐츠, 탈취된 개인 key, 잘못 설정된 OIDC/AI endpoint와 내부자 오용을 고려합니다. 폐쇄망은 인증·권한·감사와 secret 보호를 대신하지 않습니다.

## 컨테이너

- distroless static runtime
- `nonroot:nonroot`
- read-only root filesystem과 제한된 `/tmp` tmpfs
- Linux capabilities 전부 제거
- `no-new-privileges`
- loopback bind와 기관 TLS reverse proxy
- runtime에서 registry, CDN, font와 telemetry 접속 없음

## 인증과 session

- 로컬 비밀번호는 bcrypt hash로 저장합니다.
- session cookie는 HttpOnly와 SameSite=Lax를 사용하며 HTTPS로 판단된 요청에서는 Secure도 설정합니다.
- 상태 변경 session API는 CSRF cookie와 `X-CSRF-Token`을 확인합니다.
- OIDC는 Authorization Code + PKCE, state와 nonce를 검증하고 issuer/audience/expiry를 엄격히 확인합니다.
- OIDC client secret과 AI API key는 저장 후 원문을 반환하지 않습니다.

역할·권한은 인증된 요청마다 DB의 현재 값을 다시 계산하므로 기존 session에도 변경이 즉시 반영됩니다. 비밀번호 변경·관리자 초기화와 사용자 비활성화는 해당 사용자의 session을 모두 폐기합니다.

## 암호화와 root key

`MOINA_ENCRYPTION_KEY`는 정확히 32바이트이며 PostgreSQL의 OIDC/AI 비밀과 승인 snapshot 같은 보호 대상 값을 AEAD로 암호화합니다. setting key나 approval ID 같은 용도 문맥을 associated data로 결합해 ciphertext 이동 공격을 방지합니다.

Root key는 source, image, DB와 함께 저장하지 않습니다. `v0.1.4`에는 key version이나 online re-encryption 기능이 없으므로 값을 임의로 교체하면 저장 비밀 복호화와 기존 session/API key 검증이 실패합니다. DB backup과 함께 사용한 원래 key를 별도 보안 영역에 보관하고, 복구 시 같은 값을 사용합니다.

## 개인 API·MCP 키

- CSPRNG로 충분한 entropy의 token을 만들고 원문은 생성/회전 순간 한 번만 표시합니다.
- DB에는 root key에서 용도별로 분리 파생한 HMAC verifier와 짧은 prefix만 저장합니다.
- 소유자, permission, 생성·만료·최근 사용·폐기 시각을 기록합니다.
- 회전은 새 token 발급과 이전 token 무효화를 하나의 transaction으로 처리합니다.
- 역할/권한 변경은 기존 key의 유효 권한에 즉시 반영합니다.
- API와 MCP는 동일 인증·authorization·rate limit·audit 계층을 통과합니다.

## 권한과 승인

화면에서 메뉴를 숨기는 것은 편의 기능일 뿐 보안 경계가 아닙니다. 모든 API에서 서버가 현재 사용자와 key permission을 검사합니다. 관리자 권한 이름은 변경 가능하지만 `admin:access`, `users:manage`, `roles:manage`, `settings:manage`, `moderation:manage`, `audit:read`, `outbox:manage` 같은 세밀한 capability로 평가합니다.

승인 정책을 켠 경우 요청자와 승인자를 분리하고 자기 승인을 금지합니다. 승인 대상 snapshot이 변경되면 재승인을 요구합니다. 정책이 꺼진 경우 불필요한 승인 상태를 만들지 않습니다.

## 콘텐츠와 웹 보안

- Moin/Echo 본문은 React text node로 렌더링하며 임의 HTML markup을 실행하지 않습니다.
- SQL은 parameter binding을 사용합니다.
- 업로드는 body 크기를 제한하고 실제 바이트의 MIME을 식별해 허용된 이미지·영상 형식만 PostgreSQL Large Object에 고정 크기 buffer로 streaming 저장합니다.
- Content-Security-Policy, frame-ancestors, nosniff와 referrer policy를 reverse proxy와 앱에서 설정합니다.
- WebSocket handshake의 session, Origin과 권한을 확인합니다.
- 미디어 대체 텍스트는 일반 text로만 렌더링하며, Moin·프로필에 연결되지 않은 업로드는 관리자 TTL이 지난 뒤 동시 실행에 안전한 정리 작업이 삭제합니다.
- 인증된 작성 client에는 `GET /api/v1/media/config`로 현재 크기·개수·허용 MIME만 제공하고 관리자 전용 orphan TTL은 노출하지 않습니다. Client의 사전 검사는 편의 기능이며 업로드 API가 같은 정책을 다시 강제합니다.
- 사용자별 미첨부 media 100개·512 MiB quota를 advisory lock 안에서 검사해 병렬 업로드 우회를 막습니다. Large Object read는 인스턴스당 최대 8개이고 DB pool이 작으면 일반 API용 연결 5개를 남기도록 더 줄입니다.

이미지 재인코딩, EXIF 제거, 악성 파일 검사와 미디어 전용 origin은 `v0.1.4`에 포함되지 않습니다. 업로드 콘텐츠에는 기관의 별도 malware 검사 계층과 보존 정책을 적용하세요.

## OIDC·AI outbound 보호

관리자가 등록하는 OIDC issuer와 AI base URL에는 각각 독립된 `allowedHosts`와 `privateAllowedHosts`를 적용합니다.

- `allowedHosts`는 정확한 DNS 이름/IP만 허용합니다. port가 없으면 URL scheme의 기본 port(HTTPS 443, HTTP 80)에만 일치하고 비기본 port는 `host:port`로 명시해야 합니다. wildcard, scheme, 경로와 사용자 정보는 거부합니다.
- RFC1918 IPv4 또는 ULA IPv6로 해석되는 폐쇄망 endpoint는 정확한 DNS hostname을 `privateAllowedHosts`에도 등록해야 합니다. 같은 authority가 `allowedHosts`에 있어야 하며 IP literal은 사설 주소 예외로 받지 않습니다.
- HTTPS가 기본이고 폐쇄망 HTTP는 관리자가 `allowInsecureHttp`를 켠 경우에만 허용합니다.
- 최초 요청, OIDC discovery가 반환한 endpoint와 모든 redirect에서 URL·host를 다시 검사합니다.
- 연결 직전 DNS를 다시 해석하고 검증한 IP에 직접 dial해 DNS rebinding 창을 줄입니다.
- process proxy를 사용하지 않아 검증되지 않은 두 번째 hop을 만들지 않습니다.
- loopback, link-local, cloud metadata, CGNAT, unspecified와 multicast 주소는 `privateAllowedHosts`에 등록해도 항상 차단합니다.

Host allowlist는 network firewall, egress proxy 정책과 TLS 인증서 검증을 대체하지 않습니다. OIDC와 AI에 필요한 목적지만 방화벽에도 허용하고, DNS 변경과 인증서 갱신을 운영 변경 절차로 관리합니다.

`v0.1.0`에서 사설 Keycloak/OIDC·AI를 사용한 설정은 `v0.1.4` 업그레이드 시 자동으로 `privateAllowedHosts`에 들어가지 않습니다. 자동 승격은 기존 관리자 입력만으로 사설망 egress를 다시 여는 권한 확대가 되기 때문입니다. 업그레이드 전 로컬 bootstrap 최고 관리자 접근을 검증하고, 업그레이드 후 그 계정으로 정확한 hostname을 명시 저장·테스트해야 합니다.

## 이벤트와 관측 정보

업무 변경과 알림 이벤트는 같은 transaction에서 idempotency key를 가진 Outbox로 저장합니다. Worker는 lease와 `SKIP LOCKED`를 사용하고 실패 시 지수 백오프·Dead Letter를 남깁니다. Dead Letter 조회에는 `audit:read`, 상태를 바꾸는 재처리에는 별도 `outbox:manage` 권한이 필요하며 재처리는 감사 로그에 기록합니다. 조사 담당자에게 재처리 권한까지 자동 부여하지 말고 장애 복구 담당자에게만 `outbox:manage`를 부여합니다.

실시간 알림 signal은 bounded LISTEN channel이 포화되면 backpressure로 consumer를 기다립니다. Browser별 queue가 포화된 느린 WebSocket은 강제로 재연결시키고, client가 연결 시점과 60초 간격으로 durable REST unread summary를 다시 읽습니다. 따라서 WebSocket을 알림의 유일한 저장소나 전달 보장 경계로 취급하지 않습니다.

`X-Request-ID`는 길이와 문자 집합을 검증한 뒤 구조화 로그에 연결하며, 개행 등 log injection 문자는 새 서버 ID로 교체합니다. `/metrics`는 사용자 콘텐츠나 SQL문을 label에 포함하지 않지만 용량·트래픽 정보를 드러낼 수 있으므로 공개 reverse proxy에서는 차단하고 운영 수집망에만 노출합니다.

## 감사와 개인정보

관리 설정, 역할, 사용자 상태, moderation, key, 승인과 로그인 보안 사건에 감사 event ID, actor, action, 대상, 결과, 시각, IP와 user agent를 남깁니다. 다음 값은 감사 log에 넣지 않습니다.

- 비밀번호와 DSN password
- session/CSRF/OIDC token
- OIDC client secret, AI API key와 개인 key 원문
- encryption root key와 전체 ciphertext
- 필요 이상의 Moin 비공개 본문과 개인정보

## 취약점 신고

공개 이슈에 secret, 개인정보나 재현 가능한 공격 payload를 게시하지 마세요. 저장소 소유자에게 비공개 채널로 버전, 영향 범위, 최소 재현 절차와 완화책을 전달하고 key 노출이 있다면 먼저 폐기·회전합니다.
