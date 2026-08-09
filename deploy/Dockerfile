FROM golang:1.23.12 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/quota-monitor ./cmd/quota-monitor \
    && mkdir -p /out/data/firmware \
    && chmod 0700 /out/data /out/data/firmware

FROM scratch
COPY --from=build /out/quota-monitor /app/quota-monitor
# Seed named volumes with a writable directory owned by the runtime UID.
COPY --chown=65532:65532 --from=build /out/data /data
VOLUME ["/data"]
EXPOSE 8787
USER 65532:65532
ENTRYPOINT ["/app/quota-monitor"]
CMD ["server", "--listen=:8787", "--db=/data/monitor.db", "--firmware-dir=/data/firmware"]
