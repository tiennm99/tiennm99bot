FROM golang:1.26.5-alpine AS builder
WORKDIR /src

# Only the module files are copied first, keeping the download layer cached
# across ordinary source edits.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# The deploy-notify commit SHA comes from Coolify's SOURCE_COMMIT runtime env,
# not from a build arg. Keep it out of the build so Docker cache survives across
# commits. The binary is built plain.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /out/server \
    ./cmd/server

# Alpine rather than distroless/static, for one reason: /addsticker transcodes
# video and GIF sources to WEBM/VP9, which needs ffmpeg. Telegram accepts no
# other codec for a video sticker, Go's standard library has no VP9 encoder
# (golang.org/x/image/vp8 decodes only), and the binary is built CGO_ENABLED=0
# so a cgo encoder would not link either. ffmpeg is therefore a hard runtime
# dependency, and distroless/static has no way to carry it.
#
# ffmpeg comes from Alpine's own package rather than a third-party static-ffmpeg
# image, keeping the supply chain to bases already trusted by this build.
FROM alpine:3.22
RUN apk add --no-cache ffmpeg ca-certificates \
    && addgroup -g 65532 -S nonroot \
    && adduser -u 65532 -S -G nonroot nonroot
COPY --from=builder /out/server /server
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/server"]
