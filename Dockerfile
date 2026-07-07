# Shared build image for every CasperVPN Go service.
# Pass the service name via build arg; the whole repo is the context so go.work resolves.
#   docker build --build-arg SERVICE=control-plane .
ARG GO_VERSION=1.22
FROM golang:${GO_VERSION}-alpine AS build
ARG SERVICE
WORKDIR /src
COPY . .
# Build the selected service's entrypoint using the workspace.
RUN CGO_ENABLED=0 go build -o /out/app ./services/${SERVICE}/cmd/${SERVICE}

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/app /app
# PORT is read by the service at runtime (see each cmd/*/main.go).
ENTRYPOINT ["/app"]
