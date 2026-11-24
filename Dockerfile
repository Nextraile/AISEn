# ---- Stage 1: build ----
# CGO diaktifkan (onnxruntime_go memakai wrapper C via cgo) + glibc
# (libonnxruntime.so dari binary manylinux).
FROM golang:1.26-bookworm AS build

WORKDIR /src

RUN apt-get update && apt-get install -y --no-install-recommends gcc libc6-dev \
    && rm -rf /var/lib/apt/lists/*

COPY go.mod ./
RUN go mod download && go mod verify

COPY . .

ARG VERSION=0.1.0
RUN CGO_ENABLED=1 GOOS=linux go build \
        -trimpath \
        -ldflags "-s -w -X main.appVersion=${VERSION}" \
        -o /out/undip-harvester .

# ---- Stage 2: runtime ----
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists \
    && addgroup --system app && adduser --system --ingroup app app

COPY --from=build /out/undip-harvester /usr/local/bin/undip-harvester
COPY --from=build /src/lib/libonnxruntime.so /usr/local/lib/libonnxruntime.so

WORKDIR /app
RUN mkdir -p /app/data && chown -R app:app /app
VOLUME ["/app/data"]

USER app

ENV QDRANT_URL=http://qdrant:6333
ENV ONNXRUNTIME_SHARED_LIBRARY_PATH=/usr/local/lib/libonnxruntime.so

ENTRYPOINT ["/usr/local/bin/undip-harvester"]
# Tanpa argumen -> tampilkan bantuan (bukan error senyap).
CMD ["help"]
