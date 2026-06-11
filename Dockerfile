FROM golang:1.26-alpine AS builder

ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -a -installsuffix cgo \
    -ldflags="-s -w -X 'main.version=${VERSION}'" \
    -o /bin/github-actions-exporter .

FROM alpine:3.22 AS release
RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/github-actions-exporter /github-actions-exporter
ENTRYPOINT ["/github-actions-exporter"]
