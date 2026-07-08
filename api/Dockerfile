# Build context is the repo root (not api/) so this stage can reach web/ —
# a Go module can only //go:embed files inside its own module tree, so the
# SPA build has to be copied into api/internal/http/webui/dist before the
# Go build stage runs; see api/internal/http/webui/webui.go.
FROM node:22-slim AS web-build
WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN npm ci
COPY web/ .
RUN npm run build

FROM golang:1.26 AS build
WORKDIR /src
COPY api/go.mod api/go.sum* ./
RUN go mod download
COPY api/ .
COPY --from=web-build /web/dist/ ./internal/http/webui/dist/
RUN CGO_ENABLED=0 go build -o /out/dictum-api ./cmd/dictum-api

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/dictum-api /dictum-api
EXPOSE 8080
ENTRYPOINT ["/dictum-api"]
