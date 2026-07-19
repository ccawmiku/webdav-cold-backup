FROM node:24.15.0-bookworm-slim AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run lint && npm test && npm run build

FROM golang:1.26.5-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist/ /src/internal/webui/dist/
ARG VERSION=v1.0.0
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X github.com/ccawmiku/webdav-cold-backup/internal/version.Version=${VERSION}" -o /out/webdav-cold-backup ./cmd/server

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/webdav-cold-backup /webdav-cold-backup
ENV SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/webdav-cold-backup"]
