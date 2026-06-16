FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-s -w" -o /out/av-bridge-cloud ./cmd/av-bridge-cloud

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/av-bridge-cloud /av-bridge-cloud
EXPOSE 8090
ENTRYPOINT ["/av-bridge-cloud"]
