FROM golang:1.26-alpine AS build
WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY api/ api/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -o /traefik-operator ./cmd

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=build /traefik-operator /traefik-operator
USER 65532:65532
ENTRYPOINT ["/traefik-operator"]
