FROM node:22-alpine AS web
WORKDIR /app/web
ARG VERSION=dev
ENV VITE_WORKTIME_VERSION=${VERSION}
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-fund --no-audit
COPY web/ ./
RUN npm run build

FROM golang:1.25-alpine AS build
RUN apk add --no-cache ca-certificates
WORKDIR /app
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev
ARG REVISION=unknown
ARG BUILT_AT=unknown
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /app/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags "-s -w -X github.com/Siyet/worktime/internal/buildinfo.Version=${VERSION} -X github.com/Siyet/worktime/internal/buildinfo.Revision=${REVISION} -X github.com/Siyet/worktime/internal/buildinfo.BuiltAt=${BUILT_AT} -X github.com/Siyet/worktime/internal/buildinfo.Packaging=docker" \
    -o /worktime ./cmd/worktime

FROM scratch
ARG VERSION=dev
ARG REVISION=unknown
ARG BUILT_AT=unknown
LABEL org.opencontainers.image.title="WorkTime" \
      org.opencontainers.image.source="https://github.com/Siyet/worktime" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${BUILT_AT}"
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /worktime /worktime
ENV WORKTIME_DB=/data/worktime.db
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/worktime"]
