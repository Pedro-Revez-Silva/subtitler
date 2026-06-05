FROM golang:1.25-alpine AS build

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
ARG APP_VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags="-X main.version=${APP_VERSION}" -o /out/subtitler ./cmd/subtitler

FROM alpine:3.20

RUN apk add --no-cache ffmpeg ca-certificates
COPY --from=build /out/subtitler /usr/local/bin/subtitler

ENTRYPOINT ["subtitler"]
CMD ["daemon", "-config", "/config/config.yaml"]
