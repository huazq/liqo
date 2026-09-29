# zenoh-tunnel 分支 UDP adapter 合入与三集群部署验证

测试时间：2026-09-29 UTC 02:16–02:29。目标仓库为
`/home/huazq/huazq-liqo/liqo`，分支 `zenoh-tunnel`。

## 重复部署操作手册（2026-09-29 补充）

本节用于已有 201→202、201→203 Peering 和 UDP echo Deployment 的三节点环境。
下文的 `REV` **每次部署都要使用新的值**：本次实际使用 `r2`；再次执行时可用
`r3`。原因是 Gateway CR 已引用旧的自定义模板，重新 `apply` 同名模板不保证
控制器重建 Pod。通过新的模板名和镜像标签切换引用，才能明确触发滚动，并保留
上一版模板供回滚。切换一条链路期间该链路会短暂中断，先切服务端再切客户端。

在仓库根目录执行，操作者须能以 root SSH 到三节点，且本机有 Docker、Python 3、
`jq`。当前集群租户 ID、UDP 应用和 Zenoh 服务键如下；变更 Peering 后应重新查询
Gateway CR 和 echo Pod，而不能沿用这里的 ID 或 Pod IP。

| 路径 | 201 GatewayClient ID | 远端 GatewayServer ID | 服务键 | UDP echo Deployment |
| --- | --- | --- | --- | --- |
| 201→202 | `0100d236-af54-4a39-8bbb-22de0899baae` | `f24be9cd-dbcf-4097-8e0e-a8fa8e595ac3` | `liqo-wg-201-202` | 202 的 `liqo-udp-demo/udp-echo-202` |
| 201→203 | `35fd0548-7ad9-4c0d-b63e-adeb67335922` | `f24be9cd-dbcf-4097-8e0e-a8fa8e595ac3` | `liqo-wg-201-203` | 203 的 `liqo-udp-demo/udp-echo-203` |

### 1. 备份引用、构建并预载镜像

```bash
set -euo pipefail
cd /home/huazq/huazq-liqo/liqo
REV=r3  # 本次实际执行值为 r2；后续部署使用未用过的新值
TAG="zenoh-tunnel-20260929-${REV}"
IMAGE="liqo-spike/zenoh-udp-adapter:${TAG}"
WORK="$(mktemp -d /tmp/liqo-udp-redeploy.XXXXXX)"

for node in 201 202 203; do
  ssh "root@192.168.2.${node}" 'kubectl get gatewayclient,gatewayserver -A -o json' \
    > "${WORK}/gateways-${node}.json"
done
GOTOOLCHAIN=go1.25.5 go test ./test/spike/zenoh-wireguard/...
docker build -f test/spike/zenoh-wireguard/Dockerfile -t "${IMAGE}" .
docker save -o "${WORK}/adapter.tar" "${IMAGE}"
for node in 201 202 203; do
  ssh "root@192.168.2.${node}" 'k3s ctr -n k8s.io images import -' \
    < "${WORK}/adapter.tar"
done
```

`udp-adapter` 使用 `imagePullPolicy: Never`，镜像必须预载到可能调度 Gateway
Pod 的每个节点。本次 Bridge 使用现有的 `liqo-zenoh-bridge-tcp:compat-1.10`。
各 Gateway 所在 tenant namespace 必须有同集群 `liqo/zenoh-config` Secret。
本次逐集群比较 Secret 的 `.data` 哈希，四个租户副本均与本集群源 Secret 一致；
如果缺失或不同，按下面的例子把本集群 Secret 同步到对应租户 namespace，
**不要把 201 的 Secret 复制给 202/203**：

```bash
node=202
tenant=liqo-tenant-f24be9cd-dbcf-4097-8e0e-a8fa8e595ac3
ssh "root@192.168.2.${node}" 'kubectl -n liqo get secret zenoh-config -o json' |
  jq --arg ns "$tenant" '{apiVersion:"v1",kind:"Secret",metadata:{name:"zenoh-config",namespace:$ns},type:.type,data:.data}' |
  ssh "root@192.168.2.${node}" 'kubectl apply -f -'
```

### 2. 从原生模板渲染四份新模板

不要从已有的 Sidecar 模板再渲染；渲染脚本要求原生模板只含
`gateway,wireguard,geneve` 三个容器，并检查目标分支使用的单数
`--endpoint-port` 参数。先对全部模板做 API Server dry run，全部通过后再创建：

```bash
for spec in '201 client 202' '201 client 203' '202 server 202' '203 server 203'; do
  read -r node role peer <<< "$spec"
  ssh "root@192.168.2.${node}" \
    "kubectl -n liqo get wggateway${role}template wireguard-${role} -o json" \
    > "${WORK}/${node}-${role}-stock.json"
  python3 test/spike/zenoh-wireguard/scripts/render-gateway-template.py \
    --role "$role" --name "zenoh-wireguard-${role}-${peer}-${REV}" \
    --service-key "liqo-wg-201-${peer}" --adapter-image "$IMAGE" \
    --bridge-image 'liqo-zenoh-bridge-tcp:compat-1.10' \
    < "${WORK}/${node}-${role}-stock.json" \
    > "${WORK}/${node}-${role}-${peer}.json"
  ssh "root@192.168.2.${node}" 'kubectl apply --dry-run=server -f -' \
    < "${WORK}/${node}-${role}-${peer}.json"
done
for spec in '201 client 202' '201 client 203' '202 server 202' '203 server 203'; do
  read -r node role peer <<< "$spec"
  ssh "root@192.168.2.${node}" 'kubectl apply -f -' \
    < "${WORK}/${node}-${role}-${peer}.json"
done
```

### 3. 按链路滚动并验证 UDP 应用

以下命令每次只切一条链路。先确认远端新 Gateway Pod `5/5` 就绪，
再切 201 客户端，并用新镜像创建一次性 UDP Job；Job 成功要求收到与发送内容
完全相同的回包。这里查询 echo Pod 的实时 IP，不使用历史报告中的 IP。

```bash
for peer in 202 203; do
  client_id=0100d236-af54-4a39-8bbb-22de0899baae
  if [ "$peer" = 203 ]; then client_id=35fd0548-7ad9-4c0d-b63e-adeb67335922; fi
  client_ns="liqo-tenant-${client_id}"
  server_id=f24be9cd-dbcf-4097-8e0e-a8fa8e595ac3
  server_ns="liqo-tenant-${server_id}"
  ssh "root@192.168.2.${peer}" \
    "kubectl -n ${server_ns} patch gatewayserver ${server_id} --type=merge -p '{\"spec\":{\"serverTemplateRef\":{\"name\":\"zenoh-wireguard-server-${peer}-${REV}\"}}}'"
  ssh "root@192.168.2.${peer}" \
    "kubectl -n ${server_ns} rollout status deployment/gw-${server_id} --timeout=180s"
  ssh root@192.168.2.201 \
    "kubectl -n ${client_ns} patch gatewayclient ${client_id} --type=merge -p '{\"spec\":{\"clientTemplateRef\":{\"name\":\"zenoh-wireguard-client-${peer}-${REV}\"}}}'"
  ssh root@192.168.2.201 \
    "kubectl -n ${client_ns} rollout status deployment/gw-${client_id} --timeout=180s"
  echo_ip="$(ssh "root@192.168.2.${peer}" \
    "kubectl -n liqo-udp-demo get pod -l app=udp-echo-${peer} -o jsonpath='{.items[0].status.podIP}'")"
  test -n "$echo_ip" || { echo "UDP echo Pod missing on ${peer}" >&2; exit 1; }
  ssh root@192.168.2.201 \
    "kubectl -n liqo-udp-demo create job udp-probe-${REV}-${peer} --image=${IMAGE} -- /udp-probe -address=${echo_ip}:9999 -timeout=5s"
  ssh root@192.168.2.201 \
    "kubectl -n liqo-udp-demo wait --for=condition=complete job/udp-probe-${REV}-${peer} --timeout=90s && kubectl -n liqo-udp-demo logs job/udp-probe-${REV}-${peer}"
done
```

再从 201 的现有 `liqo-controller-manager` Pod 网络命名空间发送每目标 100 个
不同的 64 字节 UDP 报文，逐个校验回包并保存原始样本。`nsenter` 只进入
Pod 网络命名空间，使用宿主机 Python 运行仓库探针；先重新查询 sandbox PID。

```bash
echo_202="$(ssh root@192.168.2.202 "kubectl -n liqo-udp-demo get pod -l app=udp-echo-202 -o jsonpath='{.items[0].status.podIP}'")"
echo_203="$(ssh root@192.168.2.203 "kubectl -n liqo-udp-demo get pod -l app=udp-echo-203 -o jsonpath='{.items[0].status.podIP}'")"
sandbox="$(ssh root@192.168.2.201 \
  "k3s crictl pods --name liqo-controller-manager -o json | jq -r '.items[]|select(.state==\"SANDBOX_READY\")|.id' | head -n1")"
pid="$(ssh root@192.168.2.201 "k3s crictl inspectp ${sandbox} | jq -r .info.pid")"
scp test/spike/zenoh-wireguard/scripts/udp-rtt-probe.py root@192.168.2.201:/tmp/udp-rtt-probe.py
ssh root@192.168.2.201 \
  "nsenter -t ${pid} -n python3 /tmp/udp-rtt-probe.py --phase redeploy-${REV} --target 202=${echo_202}:9999 --target 203=${echo_203}:9999 --count 100 --interval-ms 20 --timeout-ms 1000 --output /tmp/udp-redeploy-${REV}.json"
scp "root@192.168.2.201:/tmp/udp-redeploy-${REV}.json" \
  "test/spike/zenoh-wireguard/results/2026-09-29/udp-redeploy-${REV}.json"
for node in 201 202 203; do
  ssh "root@192.168.2.${node}" \
    "kubectl get connection -A -o json | jq -r '.items[] | [.metadata.namespace,.metadata.name,.status.value] | @tsv'"
done
```

如果某链路失败，先把该链路的 201 `GatewayClient` 模板引用改回
`${WORK}/gateways-201.json` 中记录的旧值，再把远端 `GatewayServer` 改回
`${WORK}/gateways-${peer}.json` 中的旧值，逐个等待 Deployment rollout 并重跑
UDP Job。以下用备份值回滚指定链路（把 `peer` 设为 202 或 203）：

```bash
peer=202
client_id=0100d236-af54-4a39-8bbb-22de0899baae
if [ "$peer" = 203 ]; then client_id=35fd0548-7ad9-4c0d-b63e-adeb67335922; fi
server_id=f24be9cd-dbcf-4097-8e0e-a8fa8e595ac3
old_client="$(jq -er --arg id "$client_id" '.items[] | select(.metadata.name==$id) | .spec.clientTemplateRef.name' "${WORK}/gateways-201.json")"
old_server="$(jq -er --arg id "$server_id" '.items[] | select(.metadata.name==$id) | .spec.serverTemplateRef.name' "${WORK}/gateways-${peer}.json")"
test -n "$old_client" && test -n "$old_server"
ssh root@192.168.2.201 \
  "kubectl -n liqo-tenant-${client_id} patch gatewayclient ${client_id} --type=merge -p '{\"spec\":{\"clientTemplateRef\":{\"name\":\"${old_client}\"}}}'"
ssh root@192.168.2.201 \
  "kubectl -n liqo-tenant-${client_id} rollout status deployment/gw-${client_id} --timeout=180s"
ssh "root@192.168.2.${peer}" \
  "kubectl -n liqo-tenant-${server_id} patch gatewayserver ${server_id} --type=merge -p '{\"spec\":{\"serverTemplateRef\":{\"name\":\"${old_server}\"}}}'"
ssh "root@192.168.2.${peer}" \
  "kubectl -n liqo-tenant-${server_id} rollout status deployment/gw-${server_id} --timeout=180s"
```

本次 `r2` 的直接回滚值分别为客户端
`zenoh-wireguard-client-202/203`、服务端 `zenoh-wireguard-server-202/203`；
旧模板和镜像仍保留。不要删除 Peering、原生模板或 NodePort Service。

## 2026-09-29 UTC 02:55–03:00 重复部署结果

本次按上述流程使用 `REV=r2`，从本分支重新构建镜像并导入 201/202/203，
镜像 ID 为 `sha256:0d7f7b95b3bdf82fdeafdb8387abbbcfe92f3155936d64a61b03e4305ec99256`。
它与首次部署镜像 ID 相同，符合源码未变的预期；新标签、模板引用和 Pod UID
确认发生了实际重新部署。四份模板通过 API Server dry run；按 202、203
顺序切换各自服务端、客户端。四个新 Gateway Pod 均 `5/5 Running`，容器重启数
均为 0，四个 Connection 均为 `Connected`。201 的两个 `udp-probe-redeploy-r2-*`
Job 均 `Complete`，收到正确回包，单次 RTT 分别为 3.338 ms、4.726 ms。

| 目标 | 成功/发送 | 平均 RTT | p50 | p95 |
| --- | ---: | ---: | ---: | ---: |
| 201→202 | 100/100 | 3.598 ms | 2.458 ms | 6.630 ms |
| 201→203 | 100/100 | 9.875 ms | 9.193 ms | 14.642 ms |

[本次原始 200 次样本](results/2026-09-29/udp-redeploy-sidecar-r2.json)。
本次仅验证重复部署后的可用性和样本延迟，没有同期原生 WireGuard 对照，
因此不能据此计算 adapter 带来的延迟降低量。

## 首次部署：代码合入与构建

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

## 首次部署：实际部署

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

## 首次部署：UDP 应用验证

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

## 回退到原生 WireGuard 与边界

如果要退出 adapter 方案、回退到原生 WireGuard，先将 201 对应
`GatewayClient.spec.clientTemplateRef.name`
改回 `wireguard-client`，待原生客户端 Pod 就绪、UDP 探针成功后，再将远端
`GatewayServer.spec.serverTemplateRef.name` 改回 `wireguard-server`。原有
Gateway 模板和 Service NodePort 保留，切换仍会造成该路径短暂中断。

当前部署使用节点预载镜像与 `imagePullPolicy: Never`；如果 Gateway Pod
以后调度到未预载镜像的节点，需要补齐镜像或使用私有仓库。tenant namespace
中的 `zenoh-config` Secret 副本需要随原 Secret 轮换，并限制能在这些 namespace
创建 Pod 的主体。adapter 仍是单隧道 Spike，没有联合健康探针、背压或弱网性能
保证。UDP echo Deployment 与探针 Job 在验证后保留，便于复查。
