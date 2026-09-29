# WireGuard UDP over Zenoh Bridge TCP

This Spike carries one Gateway's WireGuard UDP datagrams over a framed TCP
stream. Each cross-cluster tunnel has one `udp-adapter` and one `zenoh-bridge`
container in the Gateway Pod on **each** side. All application protocols inside
that WireGuard tunnel use the same pair of adapters. The adapter handles one
tunnel only; distinct cluster pairs must use distinct Zenoh service keys.

## Build and local check

From the repository root:

```bash
go test ./test/spike/zenoh-wireguard/adapter
docker build -f test/spike/zenoh-wireguard/Dockerfile \
  -t liqo-spike/zenoh-udp-adapter:<revision> .
```

The image contains `/zenoh-udp-adapter` plus `/udp-echo` and `/udp-probe` for
data-plane checks. It is built with Go 1.25.5 and has a `scratch` runtime.

## Gateway template integration

Clone the *deployed* stock `WgGatewayClientTemplate` and
`WgGatewayServerTemplate`, then render a distinct template for each cluster
pair. For example, on the client cluster:

```bash
kubectl -n liqo get wggatewayclienttemplate wireguard-client -o json |
  python3 test/spike/zenoh-wireguard/scripts/render-gateway-template.py \
    --role client --name zenoh-wireguard-client-202 \
    --service-key liqo-wg-201-202 \
    --adapter-image liqo-spike/zenoh-udp-adapter:<revision> \
    --bridge-image liqo-zenoh-bridge-tcp:compat-1.10 \
  > /tmp/client-202-template.json
kubectl apply --dry-run=server -f /tmp/client-202-template.json
```

Repeat with `--role server` on the remote cluster, using its stock
`wireguard-server` template and the **same** service key. Use a different key
for every other cluster pair. The client WireGuard endpoint is changed to
`127.0.0.1:51820`; on the server, the adapter targets the WireGuard port from
the rendered GatewayServer resource. Both sidecars exchange data through Pod
loopback. The Bridge uses the cluster's existing `zenoh-config` Secret at
`/etc/zenoh` with stream reliability and raw TCP listener mode.

The Secret must exist in the Gateway Pod's tenant namespace. Load the pinned
adapter image onto every eligible K3s node or publish it in an approved image
registry. Preserve the stock templates for rollback. For an existing peering,
apply the custom server template and update the GatewayServer template
reference first; wait for a ready Pod. Then update the corresponding
GatewayClient template reference and verify a new WireGuard handshake and
application traffic. To roll back, restore the client reference first and
then the server reference, checking UDP reachability at each step.

The current adapter is experimental: it has no multi-tunnel demultiplexing,
combined readiness probe, bounded queue, or performance guarantee under loss.
