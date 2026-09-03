FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY api/ api/
COPY internal/ internal/

# Cross-compile from the build platform: emulating the target one to produce a
# static Go binary costs minutes per architecture and buys nothing.
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /traefik-gateway-operator ./cmd

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=build /traefik-gateway-operator /traefik-gateway-operator
USER 65532:65532
ENTRYPOINT ["/traefik-gateway-operator"]
