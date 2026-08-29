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

Root key는 source, image, DB와 함께 저장하지 않습니다. `v0.1.0`에는 key version이나 online re-encryption 기능이 없으므로 값을 임의로 교체하면 저장 비밀 복호화와 기존 session/API key 검증이 실패합니다. DB backup과 함께 사용한 원래 key를 별도 보안 영역에 보관하고, 복구 시 같은 값을 사용합니다.

## 개인 API·MCP 키

- CSPRNG로 충분한 entropy의 token을 만들고 원문은 생성/회전 순간 한 번만 표시합니다.
- DB에는 root key에서 용도별로 분리 파생한 HMAC verifier와 짧은 prefix만 저장합니다.
- 소유자, permission, 생성·만료·최근 사용·폐기 시각을 기록합니다.
- 회전은 새 token 발급과 이전 token 무효화를 하나의 transaction으로 처리합니다.
- 역할/권한 변경은 기존 key의 유효 권한에 즉시 반영합니다.
- API와 MCP는 동일 인증·authorization·rate limit·audit 계층을 통과합니다.

## 권한과 승인

화면에서 메뉴를 숨기는 것은 편의 기능일 뿐 보안 경계가 아닙니다. 모든 API에서 서버가 현재 사용자와 key permission을 검사합니다. 관리자 권한 이름은 변경 가능하지만 `admin:access`, `users:manage`, `roles:manage`, `settings:manage`, `moderation:manage`, `audit:read` 같은 세밀한 capability로 평가합니다.

승인 정책을 켠 경우 요청자와 승인자를 분리하고 자기 승인을 금지합니다. 승인 대상 snapshot이 변경되면 재승인을 요구합니다. 정책이 꺼진 경우 불필요한 승인 상태를 만들지 않습니다.

## 콘텐츠와 웹 보안

- Moin/Echo 본문은 React text node로 렌더링하며 임의 HTML markup을 실행하지 않습니다.
- SQL은 parameter binding을 사용합니다.
- 업로드는 body 크기를 제한하고 실제 바이트의 MIME을 식별해 허용된 이미지·영상 형식만 PostgreSQL에 저장합니다.
- Content-Security-Policy, frame-ancestors, nosniff와 referrer policy를 reverse proxy와 앱에서 설정합니다.
- WebSocket handshake의 session, Origin과 권한을 확인합니다.

이미지 재인코딩과 미디어 전용 origin, AI endpoint의 DNS/IP 재검증 및 redirect 차단은 `v0.1.0`에서 구현되지 않았습니다. 신뢰된 내부 AI gateway만 등록하고, outbound 방화벽으로 허용 목적지를 제한하며, 업로드 콘텐츠는 배포 전 별도 malware 처리 계층을 두는 것을 권고합니다.

## 감사와 개인정보

관리 설정, 역할, 사용자 상태, moderation, key, 승인과 로그인 보안 사건에 감사 event ID, actor, action, 대상, 결과, 시각, IP와 user agent를 남깁니다. 다음 값은 감사 log에 넣지 않습니다.

- 비밀번호와 DSN password
- session/CSRF/OIDC token
- OIDC client secret, AI API key와 개인 key 원문
- encryption root key와 전체 ciphertext
- 필요 이상의 Moin 비공개 본문과 개인정보

## 취약점 신고

공개 이슈에 secret, 개인정보나 재현 가능한 공격 payload를 게시하지 마세요. 저장소 소유자에게 비공개 채널로 버전, 영향 범위, 최소 재현 절차와 완화책을 전달하고 key 노출이 있다면 먼저 폐기·회전합니다.
