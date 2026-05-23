FROM golang:1.23-alpine AS build

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/subtitler ./cmd/subtitler

FROM alpine:3.20

RUN apk add --no-cache ffmpeg ca-certificates
COPY --from=build /out/subtitler /usr/local/bin/subtitler

ENTRYPOINT ["subtitler"]
CMD ["daemon", "-config", "/config/config.yaml"]
