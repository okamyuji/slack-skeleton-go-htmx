# syntax=docker/dockerfile:1.7

# ----------------------------------------------------------------------
# Stage 1: deps
# go.mod / go.sumだけをコピーして依存だけを先に解決します。
# ソース変更時もこのレイヤはキャッシュヒットしてビルドが速くなります。
# ----------------------------------------------------------------------
ARG GO_VERSION=1.25.11
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS deps
WORKDIR /src

RUN apk add --no-cache git ca-certificates tzdata
COPY go.mod go.sum* ./
RUN go mod download

# ----------------------------------------------------------------------
# Stage 2: builder
# ソースを取り込み、ターゲットOS/ARCH向けにstaticバイナリを作ります。
# ----------------------------------------------------------------------
FROM deps AS builder
ARG TARGETOS=linux
ARG TARGETARCH=amd64

COPY . .

ENV CGO_ENABLED=0
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ----------------------------------------------------------------------
# Stage 3: runtime
# distrolessのstaticベースでnonrootで動かします。
# テンプレートは embed.FS でバイナリに同梱されているので別途COPYは不要です。
# ----------------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot AS runtime
WORKDIR /app

COPY --from=builder /out/server /app/server
COPY --from=builder /src/migrations /app/migrations

USER nonroot:nonroot
EXPOSE 8080

ENTRYPOINT ["/app/server"]
