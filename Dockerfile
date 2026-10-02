# Frontend uses the build host so Node need not exist for every target CPU.
# Digests are Docker Official Image indexes verified on 2026-10-02.
FROM --platform=$BUILDPLATFORM docker.io/library/node:26.10.0-alpine3.24@sha256:0b36e8c136b94cd4fcf02188228e76c31ad5872eef3fec8cbd2eee500cfd9e80 AS frontend
ARG SOURCE_REVISION
WORKDIR /src/frontend
RUN test "${#SOURCE_REVISION}" -eq 40 \
    && case "$SOURCE_REVISION" in *[!0-9a-f]*) exit 1 ;; esac
COPY frontend/package.json frontend/package-lock.json ./
RUN test "$(node --version)" = v26.10.0 && npm ci
COPY frontend/ ./
COPY .nvmrc /src/.nvmrc
COPY tools/frontend-dependencies.mjs /src/tools/frontend-dependencies.mjs
COPY tools/frontenddepsverify/licenses /src/tools/frontenddepsverify/licenses
COPY internal/web/translation /src/internal/web/translation
RUN npm run build

# Compile the CGO panel on the target CPU (native or emulated), with the
# architecture-independent frontend from the same source context.
FROM --platform=$TARGETPLATFORM docker.io/library/golang:1.27.1-alpine3.24@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS builder
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
ARG SOURCE_REVISION
ARG PAIRED_DEV_BUILD=0
WORKDIR /app
RUN apk add --no-cache build-base bash curl git
COPY . .
COPY --from=frontend /src/internal/web/dist /paired-frontend
COPY --from=frontend /src/build/frontend-licenses /paired-frontend-licenses
ENV CGO_ENABLED=1 \
    CGO_CFLAGS="-D_LARGEFILE64_SOURCE" \
    PAIRED_FRONTEND_DIR=/paired-frontend \
    PAIRED_FRONTEND_LICENSE_DIR=/paired-frontend-licenses \
    PAIRED_NODE_VERSION=v26.10.0
RUN ./DockerInit.sh "$TARGETOS" "$TARGETARCH" "$TARGETVARIANT" /app/build/paired

FROM --platform=$TARGETPLATFORM docker.io/library/alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
ARG SOURCE_REVISION
LABEL org.opencontainers.image.source="https://github.com/0x3f3f3f3f3f3f3f3f3f3f3/3x-ui" \
      org.opencontainers.image.revision="$SOURCE_REVISION"
ENV TZ=Asia/Tehran
WORKDIR /app
RUN apk add --no-cache ca-certificates tzdata fail2ban bash curl openssl

# Retain every declared package resource, source/dependency license and manifest.
COPY --from=builder /app/build/paired/ /app/
RUN /app/x-ui-package verify /app >/dev/null \
    && ln -s /app/x-ui.sh /usr/bin/x-ui

RUN rm -f /etc/fail2ban/jail.d/alpine-ssh.conf \
    && cp /etc/fail2ban/jail.conf /etc/fail2ban/jail.local \
    && sed -i "s/^\[ssh\]$/&\nenabled = false/" /etc/fail2ban/jail.local \
    && sed -i "s/^\[sshd\]$/&\nenabled = false/" /etc/fail2ban/jail.local \
    && sed -i "s/#allowipv6 = auto/allowipv6 = auto/g" /etc/fail2ban/fail2ban.conf
RUN chmod +x /app/DockerEntrypoint.sh /app/x-ui /app/x-ui-package /app/x-ui.sh
ENV XUI_IN_DOCKER="true" \
    XUI_MAIN_FOLDER="/app" \
    XUI_ENABLE_FAIL2BAN="true" \
    XUI_DB_TYPE="" \
    XUI_DB_DSN=""
EXPOSE 2053
VOLUME [ "/etc/x-ui" ]
CMD [ "/app/x-ui" ]
ENTRYPOINT [ "/app/DockerEntrypoint.sh" ]
