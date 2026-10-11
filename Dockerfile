FROM golang:1.26.9-alpine AS build

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
ARG APP_VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags="-X main.version=${APP_VERSION}" -o /out/subtitler ./cmd/subtitler

FROM python:3.12-slim-bookworm
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg ca-certificates libgomp1 tesseract-ocr tesseract-ocr-eng tesseract-ocr-por \
    && rm -rf /var/lib/apt/lists/*
COPY manager/requirements.txt /opt/subtitler/requirements.txt
RUN pip install --no-cache-dir -r /opt/subtitler/requirements.txt
COPY manager /opt/subtitler
COPY --from=build /out/subtitler /usr/local/bin/subtitler

ENTRYPOINT ["subtitler"]
CMD ["daemon", "-config", "/config/config.yaml"]
