# 사설 CA bundle

사설 CA가 필요한 환경에서만 이 디렉터리에 `ca-certificates.crt`를 둡니다. 공개 루트와 사설 루트를 모두 포함한 PEM bundle이어야 하며 저장소에는 커밋하지 않습니다.

```bash
docker compose --env-file .env \
  -f deploy/docker-compose.offline.yml \
  -f deploy/docker-compose.private-ca.yml \
  up -d --pull never
```

추가 환경변수 없이 Go가 기본적으로 읽는 `/etc/ssl/certs/ca-certificates.crt`를 완전한 기관 bundle로 교체합니다.
