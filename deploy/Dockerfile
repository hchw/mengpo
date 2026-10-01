# Build the Mengpo Go API and worker from one module. The SERVICE build arg
# selects which binary is produced; runtime keeps only the selected binary and
# the tenant/platform SQL migrations it needs at startup.
FROM golang:1.24-bookworm AS build
ARG SERVICE=memory-server
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/service ./cmd/${SERVICE}

FROM m.daocloud.io/gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/service /app/service
COPY --from=build /src/db/migrations /app/db/migrations
COPY --from=build /src/models /app/models
USER nonroot:nonroot
ENTRYPOINT ["/app/service"]