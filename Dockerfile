FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod ./
COPY simulator.go ./
COPY cmd ./cmd
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /cpamc-simulator ./cmd/cpamc-simulator

FROM scratch
LABEL org.opencontainers.image.source="https://github.com/kevynsax/cpamc-simulator" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.description="Offline, secret-free cpamc simulator for local development"
COPY --from=build /cpamc-simulator /cpamc-simulator
USER 65532:65532
EXPOSE 8317
HEALTHCHECK --interval=10s --timeout=4s --start-period=3s CMD ["/cpamc-simulator", "healthcheck"]
ENTRYPOINT ["/cpamc-simulator"]
