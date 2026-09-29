# ========================================================
# Stage: Frontend (Vite)
# ========================================================
FROM --platform=$BUILDPLATFORM node:26-alpine AS frontend
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
COPY internal/web/translation /src/internal/web/translation
RUN npm run build

# ========================================================
# Stage: Builder
# ========================================================
FROM golang:1.27-alpine AS builder
WORKDIR /app
ARG TARGETARCH
ARG TARGETVARIANT
ARG SOURCE_COMMIT
ARG RELEASE_TAG=local

RUN apk --no-cache --update add \
  build-base \
  gcc \
  git \
  tar \
  gzip \
  curl \
  unzip

COPY . .
COPY --from=frontend /src/internal/web/dist ./internal/web/dist

ENV CGO_ENABLED=1
ENV CGO_CFLAGS="-D_LARGEFILE64_SOURCE"
RUN case "$SOURCE_COMMIT" in ''|*[!0-9a-f]*) exit 2 ;; esac \
  && test "${#SOURCE_COMMIT}" -eq 40 \
  && case "$TARGETARCH/$TARGETVARIANT" in \
       arm/v5|arm/v6|arm/v7) export GOARM="${TARGETVARIANT#v}" ;; \
       arm/*) echo "Unsupported ARM variant" >&2; exit 2 ;; \
     esac \
  && go build -buildvcs=false -ldflags "-w -s -X github.com/mhsanaei/3x-ui/v3/internal/config.buildSourceCommit=$SOURCE_COMMIT" -o build/x-ui .
RUN ./DockerInit.sh "$TARGETARCH" "$TARGETVARIANT" "$SOURCE_COMMIT" "$RELEASE_TAG"

# ========================================================
# Stage: Final Image of 3x-ui
# ========================================================
FROM alpine
ENV TZ=Asia/Tehran
WORKDIR /app

RUN apk add --no-cache --update \
  ca-certificates \
  tzdata \
  fail2ban \
  bash \
  curl \
  openssl

COPY --from=builder /app/build/ /app/
COPY --from=builder /app/build/x-ui.sh /usr/bin/x-ui
COPY --from=builder /app/internal/web/translation /app/internal/web/translation


# Configure fail2ban
RUN rm -f /etc/fail2ban/jail.d/alpine-ssh.conf \
  && cp /etc/fail2ban/jail.conf /etc/fail2ban/jail.local \
  && sed -i "s/^\[ssh\]$/&\nenabled = false/" /etc/fail2ban/jail.local \
  && sed -i "s/^\[sshd\]$/&\nenabled = false/" /etc/fail2ban/jail.local \
  && sed -i "s/#allowipv6 = auto/allowipv6 = auto/g" /etc/fail2ban/fail2ban.conf

RUN chmod +x \
  /app/DockerEntrypoint.sh \
  /app/x-ui \
  /usr/bin/x-ui

ENV XUI_IN_DOCKER="true"
ENV XUI_MAIN_FOLDER="/app"
ENV XUI_ENABLE_FAIL2BAN="true"
ENV XUI_DB_TYPE=""
ENV XUI_DB_DSN=""
EXPOSE 2053
VOLUME [ "/etc/x-ui" ]
CMD [ "./x-ui" ]
ENTRYPOINT [ "/app/DockerEntrypoint.sh" ]
