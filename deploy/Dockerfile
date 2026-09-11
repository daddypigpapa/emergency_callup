# SPEC §12.2: multi-stage build -> static binary + distroless image.
# /data must be a mounted volume (SPEC §12.1: no data loss on redeploy).

FROM golang:1.22-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# CGO is off: modernc.org/sqlite is pure Go, so the binary needs no libc at
# runtime, which is what lets us use distroless below (SPEC §3.2).
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/sim ./cmd/sim

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /server
COPY --from=build /out/sim /sim
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/server"]
