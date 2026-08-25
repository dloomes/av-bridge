# Cloud + downloadable bridge binaries. Two independent Go modules that
# get built in parallel and then copied into the same distroless image.
#
# Build context is the REPO ROOT (see `docker build -f av-bridge-cloud/Dockerfile .`
# in the deploy script). The old convention of running docker build from
# inside av-bridge-cloud/ no longer works because the bridge source
# has to be reachable at build time.

# ---- Bridge cross-compile (all target OS/arch) ----------------------------
# Bridge module requires Go 1.25 (per its go.mod); the cloud is still on
# 1.22. Two separate build stages let each module use the toolchain it
# actually declares without imposing an upgrade cadence on the other.
#
# Cross-compiles all three collector binaries in a single stage so
# module download + source COPY happen once. Same flags as the
# Makefile's build-* targets — statically linked (CGO_ENABLED=0),
# stripped (-s -w). version + buildTime stay "dev" here; the signed
# release builds will override via ldflags in a follow-up.
FROM golang:1.25-alpine AS bridge-build
WORKDIR /src
COPY av-bridge/go.mod av-bridge/go.sum ./
RUN go mod download
COPY av-bridge/ .
RUN CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
    go build -ldflags "-s -w" -o /out/av-bridge-windows-amd64.exe ./cmd/av-bridge \
 && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags "-s -w" -o /out/av-bridge-linux-amd64      ./cmd/av-bridge \
 && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
    go build -ldflags "-s -w" -o /out/av-bridge-linux-arm64      ./cmd/av-bridge

# ---- Cloud (native linux) build -------------------------------------------
FROM golang:1.22-alpine AS cloud-build
WORKDIR /src
COPY av-bridge-cloud/go.mod av-bridge-cloud/go.sum ./
RUN go mod download
COPY av-bridge-cloud/ .
RUN CGO_ENABLED=0 go build -ldflags "-s -w" -o /out/av-bridge-cloud ./cmd/av-bridge-cloud

# ---- Runtime image --------------------------------------------------------
FROM gcr.io/distroless/static-debian12
# Cloud binary as the entrypoint.
COPY --from=cloud-build /out/av-bridge-cloud /av-bridge-cloud
# Bridge binaries under /downloads so the /public/downloads/* handler
# can serve them via http.ServeFile. Kept as plain filesystem assets
# (not go:embed) because they belong to a different Go module and
# re-hosting them here would fight the build cache.
COPY --from=bridge-build /out/av-bridge-windows-amd64.exe /downloads/av-bridge-windows-amd64.exe
COPY --from=bridge-build /out/av-bridge-linux-amd64      /downloads/av-bridge-linux-amd64
COPY --from=bridge-build /out/av-bridge-linux-arm64      /downloads/av-bridge-linux-arm64
EXPOSE 8090
ENTRYPOINT ["/av-bridge-cloud"]
