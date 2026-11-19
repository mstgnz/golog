# Pinned to a patch release with the fix for GO-2026-6859 (net/http MaxBytesReader).
FROM golang:1.27.4-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/golog-server ./cmd \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/golog-cli ./cmd/cli

# Static binaries only: no shell, runs as an unprivileged user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ /usr/local/bin/
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/golog-server"]