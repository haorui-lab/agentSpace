FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/hellocenter ./cmd/hellocenter

FROM node:22-bookworm AS web
WORKDIR /src
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 10001 --create-home --home-dir /data hellocenter \
    && mkdir -p /data/assets \
    && chown -R hellocenter:hellocenter /data
COPY --from=build /out/hellocenter /usr/local/bin/hellocenter
COPY --from=web /src/dist /opt/hello-center/web
USER hellocenter
ENV ADDR=:8080 \
    WEB_DIR=/opt/hello-center/web \
    ASSET_DIR=/data/assets \
    REDIS_ADDR=redis:6379
EXPOSE 8080
ENTRYPOINT ["hellocenter"]
