# syntax=docker/dockerfile:1

FROM golang:1.25.5-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
COPY test/spike/zenoh-wireguard ./test/spike/zenoh-wireguard
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' \
    -o /zenoh-udp-adapter ./test/spike/zenoh-wireguard/cmd/zenoh-udp-adapter \
 && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' \
    -o /udp-echo ./test/spike/zenoh-wireguard/cmd/udp-echo \
 && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' \
    -o /udp-probe ./test/spike/zenoh-wireguard/cmd/udp-probe

FROM scratch
COPY --from=build /zenoh-udp-adapter /zenoh-udp-adapter
COPY --from=build /udp-echo /udp-echo
COPY --from=build /udp-probe /udp-probe
ENTRYPOINT ["/zenoh-udp-adapter"]
