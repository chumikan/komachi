# Development CLI and integration-test image; no frontend build is needed.
FROM golang:1.26-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2
# Existing CLI regression tests execute scripts/resolve-version.sh (Bash).
RUN apk add --no-cache bash postgresql17-client
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o /usr/local/bin/leafwiki ./cmd/leafwiki
ENTRYPOINT ["leafwiki"]
