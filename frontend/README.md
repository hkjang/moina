# MOINA frontend

React 19, TypeScript, Vite, Tailwind CSS와 Radix primitives로 만든 독립 웹 클라이언트입니다. 외부 CDN이나 런타임 웹 폰트가 없어 빌드 결과를 오프라인망에서 그대로 제공할 수 있습니다.

## 확인 명령

```bash
npm ci
npm test
VITE_MOINA_VERSION=v0.1.18 npm run build
```

개발 서버는 `/api`와 `/mcp`를 `http://127.0.0.1:8080`으로 프록시합니다. 운영에서는 Go 서버가 `dist`를 SPA fallback과 함께 제공합니다.

Moim 상세 작성기는 `visibility: "moim"`과 `moimId`를 고정 전송합니다. 모임 원문을 여는 Echo·인용 작성기도 응답의 `moimId`를 보존하며, 서버가 동일 범위를 다시 강제합니다.

전역 빠른 이동은 `Ctrl/⌘+K`로 열며 현재 권한에 맞는 화면·개인 설정·관리 메뉴, 최근 방문한 Moin·프로필·Topic·Moim과 새 Moin 작성을 한 목록에서 제공합니다. 결과는 방향키와 Enter로 선택하고, 검색어는 통합 검색으로 전달합니다. 입력 요소 밖에서는 `G F`(Flow), `G M`(Moim), `G N`(알림) 같은 연속 단축키와 `C` 작성 단축키를 사용할 수 있습니다.

## 화면 URL

공개 화면은 `/login`입니다. 로그인 후에는 URL 자체가 메뉴 상태이므로 새로고침해도 같은 화면과 query 상태가 복원됩니다.

- 소셜: `/flow`, `/moin/:id`, `/explore`, `/search`, `/pulse`, `/topics/:slug`, `/notifications`, `/pocket`, `/moims`, `/moims/:slug`, `/profile/:username`, `/ai`
- 개인화: `/settings/profile`, `/settings/feed`, `/settings/notifications`, `/settings/accessibility`, `/settings/security`, `/settings/keys`
- 관리자 공급자: `/admin/oidc`, `/admin/ai`, `/admin/smtp`
- 서비스 관리자: `/admin`, `/admin/users`, `/admin/content`, `/admin/reports`, `/admin/approvals`, `/admin/roles`, `/admin/oidc`, `/admin/ai`, `/admin/settings`, `/admin/audit`
- 상태: `/access-denied`, 존재하지 않는 URL의 한국어 404 화면

동적 캡처 URL 형식은 `/profile/{username}`, `/moin/{id}`, `/topics/{slug}`, `/moims/{slug}`입니다.

API와 보안 계약은 [API_CONTRACT.md](./API_CONTRACT.md)에 고정해 두었습니다.

Moin 작성기와 본인 게시물 수정 모달은 같은 미디어 큐를 사용합니다. 파일 선택뿐 아니라 캡처 이미지를 `Ctrl/⌘+V`로 붙여넣거나 JPEG·PNG·GIF·WebP·MP4·WebM 파일을 끌어 놓을 수 있고, 서버의 현재 개수·용량 제한과 업로드 상태를 화면에서 확인합니다.
