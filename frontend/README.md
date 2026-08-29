# MOINA frontend

React 19, TypeScript, Vite, Tailwind CSS와 Radix primitives로 만든 독립 웹 클라이언트입니다. 외부 CDN이나 런타임 웹 폰트가 없어 빌드 결과를 오프라인망에서 그대로 제공할 수 있습니다.

## 확인 명령

```bash
npm ci
npm test
VITE_MOINA_VERSION=v0.1.2 npm run build
```

개발 서버는 `/api`와 `/mcp`를 `http://127.0.0.1:8080`으로 프록시합니다. 운영에서는 Go 서버가 `dist`를 SPA fallback과 함께 제공합니다.

## 화면 URL

공개 화면은 `/login`입니다. 로그인 후에는 URL 자체가 메뉴 상태이므로 새로고침해도 같은 화면과 query 상태가 복원됩니다.

- 소셜: `/flow`, `/moin/:id`, `/explore`, `/search`, `/pulse`, `/topics/:slug`, `/notifications`, `/pocket`, `/moims`, `/moims/:slug`, `/profile/:username`, `/ai`
- 개인화: `/settings/profile`, `/settings/feed`, `/settings/accessibility`, `/settings/security`, `/settings/keys`
- 서비스 관리자: `/admin`, `/admin/users`, `/admin/content`, `/admin/reports`, `/admin/approvals`, `/admin/roles`, `/admin/oidc`, `/admin/ai`, `/admin/settings`, `/admin/audit`
- 상태: `/access-denied`, 존재하지 않는 URL의 한국어 404 화면

동적 캡처 URL 형식은 `/profile/{username}`, `/moin/{id}`, `/topics/{slug}`, `/moims/{slug}`입니다.

API와 보안 계약은 [API_CONTRACT.md](./API_CONTRACT.md)에 고정해 두었습니다.
