# MOINA Playwright E2E

이 디렉터리는 실제 애플리케이션 route와 GitHub Pages를 검사합니다. 캡처는 운영 DB가 아닌 localhost의 전용 PostgreSQL에서만 실행되며, 외부 요청·브라우저 오류·서버 5xx·가로 overflow·비밀/개인정보 패턴이 하나라도 발견되면 실패합니다.

## 애플리케이션 smoke

먼저 `moina:v0.1.17`과 테스트 PostgreSQL을 시작한 뒤 실행합니다.

```bash
npm ci --prefix e2e
npm --prefix e2e exec -- playwright install chromium
MOINA_E2E_BASE_URL=http://127.0.0.1:18080 \
MOINA_E2E_USERNAME=e2e-admin \
MOINA_E2E_PASSWORD='test-password-12345' \
MOINA_E2E_VERSION=v0.1.17 \
npm test --prefix e2e
```

전체 명령은 결정적인 빈 DB 기준의 시각 회귀와 접근성 검사를 먼저 수행한 뒤, 실제 데이터를 만드는 모임 대화·미디어 smoke를 마지막에 실행합니다. 반복 실행할 때는 전용 PostgreSQL을 새로 준비합니다.

기본 테스트는 전체 route 브라우저 smoke 뒤에 핵심 화면의 실제 DOM을 Light·Dark 및 Desktop·Mobile 조합으로 Axe 검사하고, 승인된 핵심 화면 13개의 52개 시각 베이스라인을 비교합니다. Serious·Critical 위반, 키보드 Focus Trap, 200% 확대 Reflow, Forced Colors, Reduced Motion 또는 시각 회귀가 있으면 실패하며 결과는 `e2e/test-results`에 저장됩니다.

접근성 검사만 다시 실행하려면 다음 명령을 사용합니다.

```bash
npm --prefix e2e run test:accessibility
```

시각 회귀만 실행하거나 의도한 화면 변경의 베이스라인을 승인하는 절차는 [VISUAL_REGRESSION.md](./VISUAL_REGRESSION.md)를 따릅니다. CI는 베이스라인을 읽기 전용으로 비교하며 자동 갱신하지 않습니다.

빈 전용 DB에서도 열 수 있는 모든 정적 catalog route를 직접 이동한 뒤 새로고침해 같은 URL과 정상 h1을 유지하는지 확인합니다. 데이터 ID나 slug가 필요한 상세 화면은 아래 실제 화면 캡처 단계에서 정상 API로 seed한 반환값을 사용합니다.

## 실제 화면 캡처

빈 캡처 전용 DB를 준비한 후 실행합니다. 스크립트가 로그인 session과 CSRF token을 사용해 공개 Moim과 `#MOINA #AI` 샘플 Moin을 정상 API로 생성하고, 반환된 ID/slug로 `/profile/{username}`, `/moin/{id}`, `/topics/moina`, `/moims/{slug}`를 catalog에 추가합니다. Browser smoke는 `@사용자아이디` 자동완성·키보드 삽입, Moin과 프로필의 캡처 이미지 Ctrl+V도 실제 Chromium에서 확인합니다.

```bash
MOINA_CAPTURE_BASE_URL=http://127.0.0.1:18080 \
MOINA_CAPTURE_USERNAME=capture-admin \
MOINA_CAPTURE_PASSWORD='capture-password-12345' \
MOINA_CAPTURE_VERSION=v0.1.17 \
npm --prefix e2e run capture:web
```

PATH의 FFmpeg에 `libwebp` encoder가 없다면 지원되는 실행 파일을 `MOINA_FFMPEG`로 명시합니다. 예: `MOINA_FFMPEG=/usr/bin/ffmpeg npm --prefix e2e run capture:web`

모든 route를 Light·Dark 테마 각각 `1440×1000`과 `390×844`에서 캡처합니다. 로그인·프로필 컨텍스트를 포함한 전체 PNG는 `dist/screenshots-png`에 남고, 메타데이터를 제거한 WebP와 schema v2 manifest가 `docs/assets/screenshots`에 생성됩니다. 자동 생성물을 홍보 페이지가 읽어 테마를 전환할 수 있는 실제 화면 gallery로 사용합니다.

## Pages QA

```bash
npm --prefix e2e run serve:pages &
npm --prefix e2e run test:pages
```

홍보 페이지와 두 가이드를 데스크톱·모바일에서 열어 링크, 이미지, overflow, 모바일 메뉴와 런타임 오류를 검사합니다.
