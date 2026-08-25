# Cloud + downloadable bridge binaries. Two independent Go modules that
# get built in parallel and then copied into the same distroless image.
#
# Build context is the REPO ROOT (see `docker build -f av-bridge-cloud/Dockerfile .`
# in the deploy script). The old convention of running docker build from
# inside av-bridge-cloud/ no longer works because the bridge source
# has to be reachable at build time.

# ---- Bridge (Windows amd64) cross-compile ---------------------------------
# Bridge module requires Go 1.25 (per its go.mod); the cloud is still on
# 1.22. Two separate build stages let each module use the toolchain it
# actually declares without imposing an upgrade cadence on the other.
FROM golang:1.25-alpine AS bridge-build
WORKDIR /src
COPY av-bridge/go.mod av-bridge/go.sum ./
RUN go mod download
COPY av-bridge/ .
# Cross-compile for Windows amd64 — same flags as `make build-windows`.
# The output lands in a well-known path so the final stage can just COPY
# it. version + buildTime baked in via ldflags stay "dev" here; the
# release-signed builds will override them in a follow-up.
RUN CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
    go build -ldflags "-s -w" -o /out/av-bridge-windows-amd64.exe ./cmd/av-bridge

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
# Bridge exe under /downloads so the /public/downloads/* handler can
# serve it via http.ServeFile. Kept as a plain filesystem asset (not
# go:embed) because it belongs to a different Go module and re-hosting
# it here would fight the build cache.
COPY --from=bridge-build /out/av-bridge-windows-amd64.exe /downloads/av-bridge-windows-amd64.exe
EXPOSE 8090
ENTRYPOINT ["/av-bridge-cloud"]
