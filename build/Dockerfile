# One Dockerfile for all three services; SERVICE selects the cmd/ package.
# Build context is the repository root so the shared internal/ tree is visible.
#
#   docker build -f build/Dockerfile --build-arg SERVICE=kyc -t kalapa-kyc:dev .

# --- stage 1: dependencies -------------------------------------------------
# Copying go.mod/go.sum first means the module cache layer is only invalidated
# when dependencies change, not on every source edit.
FROM golang:1.26-alpine AS deps
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

# --- stage 2: build --------------------------------------------------------
FROM deps AS build
ARG SERVICE
ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH
COPY . .
# CGO_ENABLED=0 produces a static binary, which is what lets the final stage be
# `scratch`-like distroless with no libc. -trimpath + -ldflags=-s -w strip the
# build path and symbol table: a ~30% smaller image and a reproducible build.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/app ./cmd/${SERVICE}

# --- stage 3: runtime ------------------------------------------------------
# distroless/static-debian12 has no shell, no package manager and no setuid
# binaries. That is a real control, not cosmetics: it removes the entire
# "exec into the pod and pivot" class of post-exploitation.
FROM gcr.io/distroless/static-debian12:nonroot
# CA certificates come with the base image, which is what TLS to Keycloak or an
# external API needs. UID 65532 is distroless' `nonroot` user; the chart's
# securityContext pins the same number so the two cannot drift.
COPY --from=build /out/app /app
USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/app"]
