# Build the Mengpo Go API and worker from one module. The SERVICE build arg
# selects which binary is produced; runtime keeps only the selected binary and
# the tenant/platform SQL migrations it needs at startup.
#
# Targets:
#   runtime       plain service image (default for memory-server)
#   runtime-llama runtime plus a statically linked llama-embedding binary
#                 (default for memory-worker when embedding is enabled)
FROM golang:1.24-bookworm AS build
ARG SERVICE=memory-server
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/service ./cmd/${SERVICE}

# llama.cpp embedding tool, fully statically linked so it runs on the
# distroless static runtime (no glibc on that image). GGML_OPENMP=OFF drops the
# libgomp dependency, which distroless does not ship either.
FROM golang:1.24-bookworm AS llama-build
ARG LLAMA_CPP_REPO=https://gitclone.com/github.com/ggml-org/llama.cpp
RUN apt-get update -qq \
	&& apt-get install -y -qq --no-install-recommends cmake git g++ \
	&& git clone --depth 1 ${LLAMA_CPP_REPO} /llama \
	|| git clone --depth 1 https://gh-proxy.com/https://github.com/ggml-org/llama.cpp /llama \
	&& cmake -S /llama -B /llama/build -DCMAKE_BUILD_TYPE=Release \
		-DBUILD_SHARED_LIBS=OFF -DGGML_OPENMP=OFF -DCMAKE_EXE_LINKER_FLAGS='-static' \
	&& cmake --build /llama/build --target llama-embedding -j"$(nproc)" \
	&& cp /llama/build/bin/llama-embedding /llama-embedding

FROM m.daocloud.io/gcr.io/distroless/static-debian12:nonroot AS runtime
WORKDIR /app
COPY --from=build /out/service /app/service
COPY --from=build /src/db/migrations /app/db/migrations
COPY --from=build /src/models /app/models
USER nonroot:nonroot
ENTRYPOINT ["/app/service"]

FROM runtime AS runtime-llama
COPY --from=llama-build /llama-embedding /usr/local/bin/llama-embedding
