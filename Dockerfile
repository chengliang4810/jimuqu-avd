FROM golang:1.25-bookworm AS build

WORKDIR /src

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildTime=${BUILD_TIME}" -o /out/avd ./cmd/avd

FROM debian:bookworm

ENV DEBIAN_FRONTEND=noninteractive

RUN set -eux; \
    apt-get -o Acquire::Retries=5 update; \
    for attempt in 1 2 3 4 5; do \
        apt-get -o Acquire::Retries=5 install -y --fix-missing --no-install-recommends ca-certificates ffmpeg && break; \
        if [ "$attempt" -eq 5 ]; then \
            exit 1; \
        fi; \
        sleep 5; \
    done; \
    rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=build /out/avd /usr/local/bin/avd
COPY config ./config
COPY data ./data

VOLUME ["/app/config", "/app/data"]

ENTRYPOINT ["avd"]
CMD ["-config", "/app/config/config.json"]
