# MOINA 설정 가이드

이 문서는 `v0.1.0`의 환경변수 계약과 관리자 화면에서 변경할 수 있는 설정을 구분합니다.

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

- 서비스 표시 이름, 기본 시간대와 가입 허용 여부
- session 유지 시간(5~1,440분)
- 개인 API key 인증과 MCP 활성화 여부, key별 분당 요청 한도
- 업로드 파일 한도(64 KiB~50 MiB)와 Moin당 미디어 수(1~12)

public base URL, 동시 session 수, Moin 글자 수, WebSocket 및 검색 정책은 `v0.1.0` 관리자 설정 모델에 포함되지 않습니다. 이 값이 필요한 배포는 reverse proxy 정책 또는 후속 버전에서 별도로 관리해야 합니다.

### Keycloak/OIDC

- 활성화 여부
- issuer URL
- client ID와 client secret
- redirect URL
- scopes, claim mapping과 기본 역할
- 자동 사용자 생성 여부

Client secret은 암호화해 저장하고 API 응답에 반환하지 않습니다. 빈 값은 기존 secret 유지, 명시적 삭제 동작은 제거로 구분합니다.

### AI

- 활성화 여부와 OpenAI-compatible base URL
- API style(`responses` 또는 `chat_completions`)
- API key와 model
- 기본/최대 output token(1~262,144)
- 요청 timeout(10~3,600초)

AI URL은 기본적으로 HTTPS만 허용됩니다. 폐쇄망에서 HTTP가 꼭 필요하면 관리 API의 `allowInsecureHttp`를 명시적으로 켜야 하며, 이 경우에도 신뢰된 내부 endpoint만 사용합니다. streaming이 기본이며 reverse proxy의 response buffering을 끕니다.

관리자 공통 system instruction, temperature와 사용자별 사용량 정책은 `v0.1.0` 설정 항목이 아닙니다. 필요한 경우 upstream AI gateway에서 적용하고, MOINA에 설정된 상한보다 더 좁은 한도로 운영합니다.

### 승인, 역할과 moderation

- 승인 정책 활성화, 대상 action과 approver 역할
- 변경 가능한 role/permission 묶음
- API/MCP key가 선택할 수 있는 permission 범위
- 신고 접수, 처리 상태와 사용자/게시물 제재

승인 정책이 비활성화되면 검토·승인·반려 상태와 메뉴를 제외합니다.

신고 유형·제재 단계·보존 기간을 관리자가 사용자 정의하는 정책 모델은 `v0.1.0`에 없습니다. 기관별 보존 규정은 PostgreSQL 백업·삭제 절차와 운영 문서로 관리합니다.

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

`VERSION`, Go binary, React asset, `/api/v1/version`, 로그인 화면, 프로필 메뉴와 OCI label의 값은 모두 `v0.1.0`으로 일치해야 합니다. 런타임 환경변수로 버전을 덮어쓰지 않습니다.
