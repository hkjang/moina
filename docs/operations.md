# MOINA 오프라인 운영 가이드

## 책임 경계

GitHub Release에는 `linux/amd64`용 `moina:v0.1.0` 서비스 이미지 하나를 저장한 `moina-v0.1.0.tar.gz`만 포함됩니다. PostgreSQL, reverse proxy, DNS, 인증서, backup 저장소는 운영기관이 제공합니다.

## 반입과 설치

1. 연결 구역에서 릴리스 파일과 본문 SHA256을 서로 다른 경로로 확보합니다.
2. 반입 구역에서 해시와 gzip을 확인합니다.
3. 이미지를 load하고 tag, platform, non-root와 OCI label을 검사합니다.
4. 외부 PostgreSQL을 준비하고 네 환경변수만 설정합니다.
5. `pull never`, read-only, dropped capabilities로 시작합니다.

```bash
sha256sum moina-v0.1.0.tar.gz
gzip -t moina-v0.1.0.tar.gz
gzip -dc moina-v0.1.0.tar.gz | docker image load
docker image inspect moina:v0.1.0
cp .env.example .env
chmod 600 .env
docker compose --env-file .env -f deploy/docker-compose.offline.yml up -d --pull never
```

기본 bind는 `127.0.0.1:8080`입니다. 기관 TLS reverse proxy가 HTTPS, SSE flush와 WebSocket upgrade를 전달해야 합니다.

## 상태와 관측

| Endpoint | 의미 |
| --- | --- |
| `/healthz` | 프로세스가 요청을 받을 수 있음 |
| `/readyz` | PostgreSQL migration과 필수 의존성이 준비됨 |
| `/api/v1/version` | service name, version과 프로세스 시작 시각 |

```bash
curl --fail http://127.0.0.1:8080/healthz
curl --fail http://127.0.0.1:8080/readyz
curl --fail http://127.0.0.1:8080/api/v1/version
docker compose -f deploy/docker-compose.offline.yml ps
docker compose -f deploy/docker-compose.offline.yml logs --since 15m moina
```

로그에 DSN, password, session cookie, CSRF token, OIDC/AI secret, 개인 key 원문과 Moin 비공개 본문을 남기지 않습니다. 오류 code·발생 시각과 관리자 화면의 감사 event ID를 함께 사용해 추적합니다.

## PostgreSQL backup과 복구

운영 RPO/RTO에 맞는 물리 또는 논리 backup을 사용합니다. 최소 보관 집합은 다음과 같습니다.

- PostgreSQL backup과 WAL(사용 시)
- backup 시점의 `MOINA_ENCRYPTION_KEY`
- 배포한 `moina-vX.Y.Z.tar.gz`와 SHA256
- `schema_migrations`를 포함한 schema/version 정보

복구 훈련은 격리된 PostgreSQL에 복원한 뒤 같은 encryption key로 로그인, OIDC/AI secret 복호화 여부, Moin/Link/키 목록과 audit 연속성을 확인합니다.

## 업그레이드

1. release notes와 migration의 forward/backward 호환성을 검토합니다.
2. DB와 encryption key를 backup합니다.
3. 새 tar.gz를 검증하고 load합니다.
4. staging에서 migration과 Playwright smoke를 수행합니다.
5. compose image tag를 바꾸고 서비스를 재시작합니다.
6. readiness, 버전, 로그인, Flow, 검색, 알림, 관리자 설정을 확인합니다.

새 schema를 이전 binary가 읽지 못하면 앱 image만 되돌리는 rollback은 안전하지 않습니다. migration별 rollback 계획을 별도로 승인합니다.

## 키 운영

- root encryption key는 application 관리자 계정과 분리해 vault/HSM 수준으로 보관합니다.
- 개인 API/MCP key는 사용자 화면에서 회전하고 이전 token은 즉시 폐기합니다.
- `v0.1.0`은 root key online rotation을 제공하지 않습니다. 값을 바꾸면 저장 비밀과 기존 session/API key verifier를 사용할 수 없으므로 원본을 보관하고 임의 교체하지 않습니다.
- 유출이 의심되면 관련 key 폐기, session 종료, audit 조사와 downstream secret rotation을 함께 수행합니다.

## 장애 분류

| 현상 | 확인 순서 |
| --- | --- |
| 컨테이너 반복 종료 | 필수 env 형식 → PostgreSQL DNS/TLS → migration log |
| readiness만 실패 | DB pool/권한/용량과 migration lock |
| OIDC 실패 | issuer discovery → CA → redirect URI → clock skew |
| AI streaming 지연 | endpoint 상태 → reverse proxy buffering/timeout |
| WebSocket 끊김 | upgrade header → idle timeout → origin 정책 |
| 복호화 실패 | 올바른 root key와 backup 시점 확인, 쓰기 작업 중단 |

## 제거

```bash
docker compose --env-file .env -f deploy/docker-compose.offline.yml down
```

이 명령은 외부 PostgreSQL 데이터를 삭제하지 않습니다. image와 DB를 영구 제거하려면 별도 변경 승인과 backup 보존 정책을 따릅니다.
