SHELL := /usr/bin/env bash

APP := moina
VERSION ?= $(shell tr -d '[:space:]' < VERSION)
IMAGE := $(APP):$(VERSION)
OUTPUT_DIR ?= dist

.DEFAULT_GOAL := help

.PHONY: help version fmt backend frontend test check image package verify-package offline-up offline-down pages-test clean

help: ## 사용 가능한 명령을 표시합니다.
	@awk 'BEGIN {FS = ":.*## "; printf "moina build targets\n\n"} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

version: ## 현재 버전을 표시합니다.
	@printf '%s\n' '$(VERSION)'

fmt: ## Go 형식을 검사합니다.
	@test -z "$$(gofmt -l backend)" || { gofmt -l backend; exit 1; }

backend: ## Go 서버를 빌드합니다.
	@mkdir -p out
	cd backend && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o ../out/moina ./cmd/moina

frontend: ## React 웹 앱을 빌드합니다.
	cd frontend && npm ci && VITE_MOINA_VERSION='$(VERSION)' npm run build

test: ## 백엔드와 프런트엔드 검사를 실행합니다.
	cd backend && go test ./... && go vet ./...
	cd frontend && npm ci && npm test && VITE_MOINA_VERSION='$(VERSION)' npm run build

check: ## 버전·환경변수·셸·문서 계약을 검사합니다.
	bash -n scripts/*.sh
	bash scripts/check-runtime-contract.sh
	node scripts/check-app-routes.mjs
	node scripts/check-openapi-routes.mjs
	node scripts/qa-pages.mjs
	@[[ '$(VERSION)' =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$$ ]]
	@test -f deploy/docker-compose.offline.yml
	@test -f .github/workflows/ci.yml
	@test -f .github/workflows/release.yml
	@test -f .github/workflows/pages.yml

image: ## linux/amd64 단일 서비스 이미지를 빌드합니다.
	docker build --platform linux/amd64 \
		--build-arg VERSION='$(VERSION)' \
		--build-arg VCS_REF="$$(git rev-parse --verify HEAD 2>/dev/null || printf unknown)" \
		--build-arg BUILD_DATE="$$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
		--tag '$(IMAGE)' .

package: ## 이미지를 tar.gz와 로컬 검증용 SHA256으로 패키징합니다.
	bash scripts/package-image.sh '$(VERSION)' '$(OUTPUT_DIR)'

verify-package: ## 패키지 무결성·태그·플랫폼·레이블을 확인합니다.
	bash scripts/verify-image-package.sh '$(OUTPUT_DIR)/$(APP)-$(VERSION).tar.gz' '$(IMAGE)'

offline-up: ## 네트워크 pull 없이 서비스를 시작합니다.
	docker compose --env-file .env -f deploy/docker-compose.offline.yml up -d --pull never

offline-down: ## 데이터 삭제 없이 서비스를 중지합니다.
	docker compose --env-file .env -f deploy/docker-compose.offline.yml down

pages-test: ## 정적 홍보·가이드 페이지를 브라우저로 검사합니다.
	npm ci --prefix e2e
	npm --prefix e2e run test:pages

clean: ## 생성 산출물을 삭제합니다.
	rm -rf out dist frontend/dist e2e/test-results e2e/playwright-report
