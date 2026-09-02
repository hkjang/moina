# syntax=docker/dockerfile:1@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32

ARG NODE_IMAGE=node:24-alpine@sha256:e67514e5d0f6c46656005e1b693b2ec9d52e80b641307de684d4a015ba7a4eaf
ARG GO_IMAGE=golang:1.26.6-alpine@sha256:3889b425f035be855a72fb4755265311293b6d414521f0a519d819df32222d83
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

FROM ${NODE_IMAGE} AS frontend-builder
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
ARG VERSION=v0.1.13
RUN npm test && VITE_MOINA_VERSION="${VERSION}" npm run build

FROM ${GO_IMAGE} AS backend-builder
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download && go mod verify
COPY backend/ ./
ARG VERSION=v0.1.13
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN go test ./... && go vet ./... && \
    CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/moina ./cmd/moina

FROM ${RUNTIME_IMAGE} AS runtime
ARG VERSION=v0.1.13
ARG VCS_REF=unknown
ARG BUILD_DATE=unknown
LABEL org.opencontainers.image.title="moina" \
      org.opencontainers.image.description="오프라인 운영 가능한 AI 소셜 지식 네트워크" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${VCS_REF}" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.source="https://github.com/hkjang/moina" \
      org.opencontainers.image.licenses="MIT"
WORKDIR /app
COPY --from=backend-builder --chown=nonroot:nonroot /out/moina /app/moina
COPY --from=frontend-builder --chown=nonroot:nonroot /src/frontend/dist /app/web/dist
USER nonroot:nonroot
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 CMD ["/app/moina", "healthcheck"]
ENTRYPOINT ["/app/moina"]
