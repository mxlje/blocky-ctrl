# Cross-compile on the build machine's architecture instead of emulating the target.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS TARGETARCH TARGETVARIANT VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /blocky-ctrl .

# Static binary on distroless: CA certificates, a non-root user, nothing else.
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /blocky-ctrl /blocky-ctrl
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s CMD ["/blocky-ctrl", "healthcheck"]
ENTRYPOINT ["/blocky-ctrl", "--config", "/config/blocky-ctrl.yml"]
