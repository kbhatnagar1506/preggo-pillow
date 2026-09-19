# The whole product is one cgo-free binary, so the image is the binary and a
# CA bundle — nothing else. No package manager, no shell, no interpreter.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /lull ./cmd/lull

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /lull /lull
# Cloud Run assigns $PORT; the binary reads it. The database lives under /tmp
# because a container filesystem is read-only and a demo instance keeps
# nothing — every restart starts a fresh simulated pregnancy, which is the
# honest behaviour for a public demo.
ENV PORT=8080
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/lull"]
CMD ["-source", "sim", "-db", "/tmp/lull.db", "-owner", "demo"]
