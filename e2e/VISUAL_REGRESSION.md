# MOINA 시각 회귀 테스트

`visual-regression.mjs`는 핵심 화면 13개를 Light·Dark 테마와 데스크톱 `1440×1000`·모바일 `390×844` 조합에서 비교합니다. 따라서 한 번의 실행에서 총 52개 베이스라인을 검증합니다. 전체 문서 갤러리 캡처와 달리, 이 테스트는 빈 전용 DB에서도 결정적으로 표시되는 핵심 경로만 대상으로 합니다.

## 비교 실행

애플리케이션과 전용 PostgreSQL을 시작하고 Chromium을 설치한 뒤 실행합니다.

```bash
MOINA_VISUAL_BASE_URL=http://127.0.0.1:18080 \
MOINA_VISUAL_USERNAME=ci-admin \
MOINA_VISUAL_PASSWORD='test-password-12345' \
node e2e/visual-regression.mjs
```

비교는 픽셀별 최대 채널 차이가 `24`를 초과한 픽셀의 비율을 계산하며, 기본 허용치는 전체 픽셀의 `0.5%`입니다. 필요하면 `MOINA_VISUAL_PIXEL_THRESHOLD`(0~255)와 `MOINA_VISUAL_MAX_DIFF_RATIO`(0~1)를 명시해 조정할 수 있습니다. 실패 결과는 `e2e/test-results/visual`에 `*-actual.png`, `*-diff.png`, `visual-regression.json`으로 남습니다.

## 의도한 변경의 베이스라인 승인

베이스라인은 테스트 중 자동 생성하거나 덮어쓰지 않습니다. UI 변경을 검토하고 승인한 경우에만 다음과 같이 명시적으로 갱신합니다.

```bash
MOINA_VISUAL_BASE_URL=http://127.0.0.1:18080 \
MOINA_VISUAL_USERNAME=ci-admin \
MOINA_VISUAL_PASSWORD='test-password-12345' \
MOINA_UPDATE_VISUALS=1 \
node e2e/visual-regression.mjs
```

갱신 후 `e2e/visual-baselines/manifest.json`과 52개 PNG의 변경을 실제·Diff 이미지와 함께 검토해 커밋합니다. CI에서 실수로 베이스라인이 바뀌지 않도록 갱신은 기본 차단되며, 특별한 재생성 작업에서만 `MOINA_ALLOW_CI_VISUAL_UPDATE=1`을 추가할 수 있습니다.

릴리스에 커밋하는 승인 베이스라인의 기준 renderer는 CI의 `ubuntu-24.04`와 lockfile에 고정된 Playwright Chromium입니다. 같은 Chromium 버전이어도 OS font package와 rasterizer가 다르면 모든 화면에 픽셀 차이가 생길 수 있으므로, 로컬 갱신 결과만으로 승인하지 않습니다. UI 변경 후 CI가 실패하면 `moina-ci-diagnostics` artifact의 `*-actual.png`와 `*-diff.png`를 검토하고, 동일 CI renderer에서 생성된 actual과 manifest SHA-256을 함께 반영한 뒤 CI 비교가 52개 모두 통과하는지 확인합니다.

## 결정성 계약

- Locale은 `ko-KR`, 시간대는 `Asia/Seoul`, device scale은 `1`, 모션은 `reduce`로 고정합니다.
- 애니메이션·transition·caret·scroll 위치를 고정합니다.
- `time`, 상대 시각, ISO 시각, UUID, 현재 테스트 사용자명은 캡처 전에 정규화합니다.
- Avatar와 `data-visual-mask` 요소는 Warm Neutral 고정색으로 마스킹합니다.
- 앱 외부 HTTP(S) 요청, 브라우저 콘솔 오류, 서버 5xx, 가로 overflow, 오류 화면은 비교 전에 실패합니다.
- 베이스라인 SHA-256과 Playwright Chromium 버전은 manifest에 기록합니다.

지원 시나리오 목록은 애플리케이션을 실행하지 않고도 확인할 수 있습니다.

```bash
node e2e/visual-regression.mjs --list
```
