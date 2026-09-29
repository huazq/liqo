# zenoh-tunnel 分支 UDP adapter 合入与三集群部署验证

测试时间：2026-09-29 UTC 02:16–02:29。目标仓库为
`/home/huazq/huazq-liqo/liqo`，分支 `zenoh-tunnel`。

## 代码合入与构建

从 `liqo-multi-listener-backend` 的 UDP adapter Spike 合入 `adapter`、三个
`cmd` 程序和 Dockerfile；增加适配目标分支 Gateway 模板的
[`render-gateway-template.py`](scripts/render-gateway-template.py)。未发现同路径的
Git 文本冲突。目标分支客户端 WireGuard 参数为 `--endpoint-port`，与旧部署脚本
使用的 `--endpoint-ports` 不同；新渲染脚本读取当前集群的原生模板、要求存在
`--endpoint-port`，再将其改为 `51820`。保留原生模板供回滚。

```bash
GOTOOLCHAIN=go1.25.5 go test ./test/spike/zenoh-wireguard/...
docker build -f test/spike/zenoh-wireguard/Dockerfile \
  -t liqo-spike/zenoh-udp-adapter:zenoh-tunnel-20260929 .
```

Go 测试通过。镜像 ID 为
`sha256:0d7f7b95b3bdf82fdeafdb8387abbbcfe92f3155936d64a61b03e4305ec99256`，
已导入三个 K3s 节点，实际运行的 `udp-adapter` 容器也显示此镜像 ID。Bridge
沿用节点已有的 `liqo-zenoh-bridge-tcp:compat-1.10`。

## 实际部署

| 路径 | 201 客户端 Gateway | 远端服务端 Gateway | Zenoh 服务键 |
| --- | --- | --- | --- |
| 201→202 | `liqo-tenant-0100d236-af54-4a39-8bbb-22de0899baae` 中 `gw-0100d236-af54-4a39-8bbb-22de0899baae` | 202 上 `liqo-tenant-f24be9cd-dbcf-4097-8e0e-a8fa8e595ac3` 中 `gw-f24be9cd-dbcf-4097-8e0e-a8fa8e595ac3` | `liqo-wg-201-202` |
| 201→203 | `liqo-tenant-35fd0548-7ad9-4c0d-b63e-adeb67335922` 中 `gw-35fd0548-7ad9-4c0d-b63e-adeb67335922` | 203 上同名 tenant namespace 中 `gw-f24be9cd-dbcf-4097-8e0e-a8fa8e595ac3` | `liqo-wg-201-203` |

每个 Gateway Pod 保留原有 `gateway,wireguard,geneve`，新增 `udp-adapter` 与
`zenoh-bridge`，共 5 个容器。201 上的两个客户端分别引用
`zenoh-wireguard-client-202/203`，202/203 服务端分别引用
`zenoh-wireguard-server-202/203`。四份模板经 API Server dry run 后创建，
再逐条将已有 GatewayServer、GatewayClient 的模板引用切换过去；未重建
Peering 或改变原有 NodePort（202 为 31301，203 为 30743）。

各 Gateway tenant namespace 均复制了所属集群的 `liqo/zenoh-config` Secret，
供 Bridge 只读挂载。客户端 WireGuard endpoint 为 `127.0.0.1:51820`，
服务端 WireGuard 仍监听 `51840`，服务端 adapter 把 UDP 帧送至
`127.0.0.1:51840`；两端 Bridge 用不同的服务键区分隧道。

## UDP 应用验证

原环境没有 UDP echo 应用，因此在 202、203 的 `liqo-udp-demo` namespace 中
分别部署 `udp-echo-202/203`，测试时 Pod IP 为 `10.44.0.72`、`10.46.0.69`，
均为 `1/1 Running`。201 的 `liqo-udp-demo` namespace 中创建 UDP 探针 Job。
切换前原生 WireGuard 路径的两个 Job 均 `Complete`；切换后两个 Job 也均
`Complete`，回包内容正确，单次往返分别为 **3.301 ms** 和 **3.172 ms**。

为了排除单包偶然成功，从 201 的 `liqo-controller-manager` Pod 网络命名空间
再对两个 UDP echo 应用各发送 100 个不同的 64 字节报文，逐个校验回包：

| 目标 | 成功/发送 | 平均 RTT | p50 | p95 |
| --- | ---: | ---: | ---: | ---: |
| 201→202 | 100/100 | 6.667 ms | 7.236 ms | 11.865 ms |
| 201→203 | 100/100 | 10.545 ms | 9.199 ms | 14.886 ms |

[原始 200 次样本](results/2026-09-29/udp-sidecar-201-to-202-203.json)与
[探针源码](scripts/udp-rtt-probe.py)一并保存。这轮只验证 Sidecar 路径的
可用性；原生阶段仅做单包 smoke test，不构成严谨的延迟差值对照。

最终核验中，四个 Gateway Pod 均为 `5/5 Running`、全部容器重启次数为 0，
四个 `Connection` 均为 `Connected`。两条客户端 WireGuard peer endpoint
均为 `127.0.0.1:51820`，两条服务端 peer endpoint 均为
`127.0.0.1:55002`，两端有握手与双向传输计数。Bridge 日志显示两条服务键
各自建立 Zenoh Session。202↔203 没有直接 Gateway 连接，本次没有测试该方向。

## 回滚与边界

回滚某条路径时，先将 201 对应 `GatewayClient.spec.clientTemplateRef.name`
改回 `wireguard-client`，待原生客户端 Pod 就绪、UDP 探针成功后，再将远端
`GatewayServer.spec.serverTemplateRef.name` 改回 `wireguard-server`。原有
Gateway 模板和 Service NodePort 保留，切换仍会造成该路径短暂中断。

当前部署使用节点预载镜像与 `imagePullPolicy: Never`；如果 Gateway Pod
以后调度到未预载镜像的节点，需要补齐镜像或使用私有仓库。tenant namespace
中的 `zenoh-config` Secret 副本需要随原 Secret 轮换，并限制能在这些 namespace
创建 Pod 的主体。adapter 仍是单隧道 Spike，没有联合健康探针、背压或弱网性能
保证。UDP echo Deployment 与探针 Job 在验证后保留，便于复查。
