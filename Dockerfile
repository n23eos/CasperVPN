# Shared build image for every CasperVPN Go service.
# Pass the service name via build arg; the whole repo is the context so go.work resolves.
#   docker build --build-arg SERVICE=control-plane .
ARG GO_VERSION=1.22
FROM golang:${GO_VERSION}-alpine AS build
ARG SERVICE
WORKDIR /src
# Dependency layer first: module files change far less often than code, so
# day-to-day builds reuse the download cache.
COPY go.work go.work
COPY packages/contracts/go.mod packages/contracts/
COPY packages/platform/go.mod packages/platform/
COPY services/control-plane/go.mod services/control-plane/go.sum* services/control-plane/
COPY services/subscription/go.mod services/subscription/go.sum* services/subscription/
COPY services/delivery/go.mod services/delivery/go.sum* services/delivery/
COPY services/billing/go.mod services/billing/go.sum* services/billing/
COPY services/telemetry/go.mod services/telemetry/go.sum* services/telemetry/
COPY services/orchestrator/go.mod services/orchestrator/go.sum* services/orchestrator/
RUN go mod download all || true
COPY . .
# Build the selected service's entrypoint using the workspace.
# -trimpath + stripped symbols: reproducible paths, smaller binary.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./services/${SERVICE}/cmd/${SERVICE}

# nonroot tag: distroless defaults to uid 0; none of the services need root
# (they bind high ports and touch no host paths).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
# PORT is read by the service at runtime (see each cmd/*/main.go).
USER nonroot
ENTRYPOINT ["/app"]
