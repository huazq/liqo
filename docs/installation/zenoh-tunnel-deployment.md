# Liqo + Zenoh Bridge TCP 完整部署流程

本手册部署 `zenoh-tunnel` 分支的 Liqo。目标是让所有 Liqo 到**远端 Kubernetes API Server** 的控制面 TCP 请求（包括普通 REST、watch、exec/attach/port-forward SPDY）经 `zenoh-bridge-tcp`（ZBT）传输；Pod 数据面仍使用 Liqo Fabric、GENEVE 和 WireGuard。

本文的三集群示例为：

| 集群 | 节点地址 | Pod CIDR | 集群 ID |
| --- | --- | --- | --- |
| 201 | `192.168.2.201` | `10.42.0.0/16` | `f24be9cd-dbcf-4097-8e0e-a8fa8e595ac3` |
| 202 | `192.168.2.202` | `10.44.0.0/16` | `0100d236-af54-4a39-8bbb-22de0899baae` |
| 203 | `192.168.2.203` | `10.46.0.0/16` | `35fd0548-7ad9-4c0d-b63e-adeb67335922` |

将地址、CIDR、Cluster ID、镜像仓库和证书路径替换为实际值。全新安装会生成新的 Liqo Cluster ID，
因此上表仅是本次已验证环境的记录；不要在生产环境复用其中的 Cluster ID、镜像 tag 或 NodePort。

## 1. 设计和前置条件

每个集群需要：

1. 一个可用的 Kubernetes 集群和不重叠的 Pod/Service CIDR。
2. 一个已部署、启用认证和 ACL 的 Zenoh router 网络；所有 Liqo ZBT Pod 必须能加入它。
3. 可访问的 Gateway Endpoint（用于 WireGuard 数据面）以及 ZBT listener NodePort（用于首次 `liqoctl peer/authenticate` 引导）。
4. 管理员 kubeconfig；对端管理员 kubeconfig 仅用于显式 bootstrap，不会被 Liqo 运行时直接使用。
5. 一致的 Liqo/ZBT 镜像版本和同一份兼容的 Zenoh 配置策略。

安全要求：ZBT listener 不应向不可信网络暴露；Zenoh 配置必须使用 mTLS/认证和最小化 key expression ACL。`RemoteAPIAccess` 在 listener 尚未就绪时会阻止 direct 回退。

## 2. 构建 Liqo 镜像

在 `zenoh-tunnel` 分支构建至少以下组件：

```bash
git checkout zenoh-tunnel
git rev-parse --short HEAD

# 标准构建脚本：适用于可推送到镜像仓库的环境。
export DOCKER_REGISTRY=registry.example.com
export DOCKER_ORGANIZATION=liqo
export DOCKER_TAG=zenoh-$(git rev-parse --short HEAD)
export ARCHS=linux/amd64
export DOCKER_PUSH=true

./build/liqo/build.sh ./cmd/liqo-controller-manager
./build/liqo/build.sh ./cmd/crd-replicator
./build/liqo/build.sh ./cmd/virtual-kubelet
```

离线或 K3s 本地镜像模式下，构建后将镜像导入每个节点：

```bash
docker save <controller-image> <crd-replicator-image> <virtual-kubelet-image> \
  | ssh root@<node> 'k3s ctr images import -'
```

镜像 tag 必须同时写入 Helm values；不要只更新 Controller Manager 而留下旧 Virtual Kubelet 镜像。

## 3. 准备每个集群的 Zenoh 配置 Secret

`config.json5` 以及它引用的 CA、证书和私钥必须位于同一个 Secret 中。示例：

```bash
kubectl -n liqo create secret generic zenoh-config \
  --from-file=config.json5=./zenoh/config.json5 \
  --from-file=ca.pem=./zenoh/ca.pem \
  --from-file=cert.pem=./zenoh/cert.pem \
  --from-file=key.pem=./zenoh/key.pem
```

配置文件必须允许：

- backend 发布/订阅本集群 ID 对应的 API key expression；
- listener 接受来自已授权对端的 raw TCP 流；
- ZBT 到本地 Kubernetes Service `default/kubernetes:443` 的访问。

## 4. 安装 CRD 和 RBAC

Helm 不会自动安装其 dependency chart 中 `crds/` 目录的 CRD。因此全新安装必须先安装**全量**
Liqo CRD（不能只安装新增的 `RemoteAPIAccess` CRD），再启动启用 Zenoh 的 Controller Manager：

```bash
kubectl apply --server-side --force-conflicts \
  -f deployments/liqo/charts/liqo-crds/crds/
```

所有集群都要执行。Controller Manager 的 ClusterRole 是 Helm chart 渲染出的对象；
`deployments/liqo/files/liqo-controller-manager-ClusterRole.yaml` 仅是模板的 rules 片段，不能直接
`kubectl apply`。确认：

```bash
kubectl get crd remoteapiaccesses.core.liqo.io virtualnodes.offloading.liqo.io
```

必须使用 server-side apply：`wggateway*`、`shadowpods` 和 `virtualnodes` 等 CRD 的 OpenAPI
schema 较大，客户端 apply 写入的 `kubectl.kubernetes.io/last-applied-configuration` annotation 会超过
Kubernetes 的 262144 字节上限。

### 完全重装前的清理顺序

如需删除已有 Liqo 后重新安装，先备份 `liqo/zenoh-config`，并保留独立的 Zenoh
基础设施命名空间。不要阻塞等待 `liqo` namespace 自然删除：Liqo CR 的 finalizer 或已删除
API group 的 stale discovery 可能使其卡住。对已确认不再需要的 Liqo 环境，正确顺序是：

1. 在每个 `liqo*` namespace 内删除所有可列举的 namespaced 资源（包括 Helm release Secret、
   内建对象和 Liqo CR），让 API server 能完成正常垃圾回收；
2. 在**所有** namespace 中删除 `*.liqo.io` CR（Liqo IP/Configuration 等可能位于业务或测试
   namespace）；若对象自身被 finalizer 卡住，清空该对象的 `metadata.finalizers`；
3. 删除残留的 `liqo-webhook` Validating/MutatingWebhookConfiguration；否则已删 webhook Service
   会阻止对残留 CR finalizer 的 patch；
4. 对 namespace 发起异步删除，并优先等待其自然删除；
5. 仅当 namespace 在对象清扫后仍处于 `Terminating`，才调用 `/finalize` 并清空
   `spec.finalizers`；
6. 删除所有 `*.liqo.io` CRD；若 CRD 卡在 `customresourcecleanup.apiextensions.k8s.io`，清空该
   **CRD 本身**的 `metadata.finalizers`；
7. 审计 `liqo*` namespace、所有 Liqo CR 和 `*.liqo.io` CRD 均为零后，才安装新的 CRD。

`/finalize` 会放弃 namespace 内仍可见对象的正常 finalizer 清理，故仅适用于完整重装，且命令
必须精确匹配 `liqo*`，不得匹配 `zenoh-system` 或其他业务 namespace。

```bash
# 仅示意；先备份 zenoh-config，且不要匹配 zenoh-system。
for ns in $(kubectl get ns -o json | jq -r \
  '.items[] | select(.metadata.name | startswith("liqo")) | .metadata.name'); do
  # 仅作用于 Liqo namespace；先删对象，避免 /finalize 后旧对象在重建同名 namespace 时重新可见。
  kubectl api-resources --namespaced=true --verbs=list -o name | sort -u \
    | xargs -r -P 16 -n 1 sh -c \
      'kubectl -n '"$ns"' delete "$0" --all --ignore-not-found --wait=false || true'
  kubectl delete namespace "$ns" --ignore-not-found --wait=false
  # 给予正常 GC 一段时间；仍存在才强制最终化。
  sleep 30
  if kubectl get ns "$ns" >/dev/null 2>&1; then
    kubectl get ns "$ns" -o json | jq '.spec.finalizers=[]' \
      | kubectl replace --raw "/api/v1/namespaces/$ns/finalize" -f -
  fi
done

# 删除跨 namespace 的 Liqo CR；不会删除 zenoh-system 或业务 namespace 中的其他对象。
# 先移除失效的 Liqo admission webhook，避免它引用已删除的 liqo-webhook Service。
kubectl get validatingwebhookconfigurations,mutatingwebhookconfigurations -o name \
  | grep 'liqo' | xargs -r kubectl delete
for resource in $(kubectl api-resources --verbs=list -o name | grep '\.liqo\.io$'); do
  kubectl delete "$resource" -A --all --ignore-not-found --wait=false
  kubectl get "$resource" -A -o json 2>/dev/null \
    | jq -r '.items[]? | "\(.metadata.namespace) \(.metadata.name)"' \
    | while read -r crns crname; do
        kubectl -n "$crns" patch "$resource" "$crname" --type=merge \
          -p '{"metadata":{"finalizers":[]}}' || true
      done
done

kubectl get crd -o json | jq -r \
  '.items[] | select(.metadata.name | endswith(".liqo.io")) | .metadata.name' \
  | xargs -r kubectl delete crd --wait=false
# 仅当 CRD 已长期卡住时使用：
kubectl patch crd <terminating-liqo-crd> --type=merge -p '{"metadata":{"finalizers":[]}}'
```

## 5. 创建 Helm values

为每个集群创建独立 values 文件，例如 `values-201.yaml`：

```yaml
# 此 chart 的 Chart.AppVersion 为空，tag 必填；基础未改动组件使用发布版本。
tag: v1.2.0
pullPolicy: IfNotPresent

apiServer:
  address: 192.168.2.201

ipam:
  podCIDRs:
    - 10.42.0.0/16
  # K3s 默认值；必须与 kube-apiserver 的 --service-cidr 一致。
  serviceCIDR: 10.43.0.0/16
  reservedSubnets:
    - 192.168.2.0/24

controlPlane:
  transport: zenoh
  zenoh:
    bridgeImage: registry.example.com/liqo/zenoh-bridge-tcp:<tag>
    configSecretName: zenoh-config
    configSecretKey: config.json5

networking:
  enabled: true
  genevePort: 6091
  fabric:
    # 显式固定与当前 chart 参数集兼容的镜像；本地模式不能回退到发布版 fabric。
    image:
      name: docker.io/library/fabric
      version: zenoh-<fabric-git-sha>
    config:
      ping:
        port: 54321
        # chart 会无条件传入这些 flag，不能留空。
        lossThreshold: 5
        interval: 2s
        updateStatusInterval: 10s
```

202、203 分别替换 `apiServer.address`、`ipam.podCIDRs` 和本地 `reservedSubnets`；`ipam.serviceCIDR` 也必须与每个集群 API server 的 `--service-cidr` 一致。若使用本地镜像，显式设置 `controllerManager.image`、`crdReplicator.image`、`virtualKubelet.image` 的仓库和 version，使其与已导入的 tag 一致；保留全局 `tag: v1.2.0` 供未改动组件使用。

渲染检查必须先于部署：

```bash
helm template liqo deployments/liqo -n liqo -f values-201.yaml > /tmp/liqo-201.yaml
grep -E 'controlplane-transport|zenoh-bridge-image|ping-port' /tmp/liqo-201.yaml
```

本分支 v1.2.0 的 Controller Manager 参数为 `--enable-api-server-ip-remapping`。不要从其他 Liqo 版本复制 `--enable-api-server-proxy-ip-remapping`、`--default-resource-slice-class-enabled`、`--podcidr`、`--vk-options-default-template` 或 `--gateway-template-watch-enabled`。

## 6. 安装或升级 Liqo

在每个集群执行：

```bash
# K3s 节点上使用其管理员 kubeconfig；其他发行版使用当前 kubectl context 或显式 KUBECONFIG。
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
helm upgrade --install liqo deployments/liqo \
  --namespace liqo --create-namespace \
  -f values-201.yaml \
  --wait --timeout 10m
```

升级已有环境前，先备份值和资源：

```bash
helm get values liqo -n liqo -a > backup-values.yaml
kubectl get remoteapiaccesses -n liqo -o yaml > backup-remoteapiaccesses.yaml
```

检查基础组件：

```bash
kubectl -n liqo rollout status deployment/liqo-controller-manager --timeout=5m
kubectl -n liqo rollout status deployment/liqo-crd-replicator --timeout=5m
kubectl -n liqo get pods
```

## 7. 预置 ZBT 路径，并通过它建立对等关系

每个集群为每个需要访问的远端集群创建一个 `RemoteAPIAccess`。它会在**本集群**创建一个
listener（每个远端一个）和一个共享 backend；listener 经 Zenoh 路由到远端集群的 backend，再由
backend 访问该远端的 `default/kubernetes:443`。因此首次管理员操作不需要 direct API 路径。

在创建资源前，先读取每个**当前安装**的 Cluster ID；不能使用重装前的值：

```bash
kubectl -n liqo get configmap liqo-clusterid-configmap \
  -o jsonpath='{.data.CLUSTER_ID}{"\n"}'
```

例如，201 要访问 202，先在 201 创建以下对象；202 同时也必须至少有一个
`RemoteAPIAccess`（例如 202 访问 201），以创建供 201 请求到达的 backend：

```yaml
apiVersion: core.liqo.io/v1beta1
kind: RemoteAPIAccess
metadata:
  name: remote-0100d236-af54-4a39-8bbb-22de0899baae
  namespace: liqo
spec:
  remoteClusterID: 0100d236-af54-4a39-8bbb-22de0899baae
```

在三集群示例中，创建方向为 `201 -> 202`、`202 -> 201`、`201 -> 203`、`203 -> 201`。
等待所有对象 `Ready=True` 后，获取**发起 liqoctl 命令的本集群** listener NodePort：

```bash
kubectl -n liqo get remoteapiaccesses
kubectl -n liqo get service liqo-zenoh-api-listener-0100d236-af54-4a39-8bbb-22de0899baae \
  -o jsonpath='{.spec.ports[0].nodePort}{"\n"}'
```

再从 201 执行指向 202 的 peer；`--remote-zenoh-listener-address` 是 201 自己 listener 的
可达 `host:port`，而不是 202 的 NodePort：

```bash
liqoctl peer \
  --remote-kubeconfig ./kubeconfig-202-admin.yaml \
  --remote-zenoh-listener-address 192.168.2.201:<201-listener-for-202-nodeport>
```

如果先进行 authenticate，再执行 peer，则两个命令均传入同一方向的本地 listener 地址。

运行时不依赖 NodePort：Identity Secret 被标注为集群内 listener Service FQDN，client-go 保留远端 kubeconfig `server`，只将 TCP dial 定向到本地 ZBT listener。TLS 的目标名称、证书 CA 和 API server `Host` 均不改变。

## 8. 控制面验收

在每个集群检查：

```bash
kubectl -n liqo get remoteapiaccesses -o wide
kubectl -n liqo get deploy | grep zenoh-api
kubectl -n liqo get secret -l liqo.io/remote-cluster-id -o yaml | grep zenoh-controlplane-listener
kubectl get connections.networking.liqo.io -A -o wide
```

验收条件：

- 每个已 peer 的远端集群都有一个 `RemoteAPIAccess`，`Ready=True`；
- `liqo-zenoh-api-backend` 和对应 listener Deployment Ready；
- Identity Secret 有 `liqo.io/zenoh-controlplane-listener` 和 required 注解；
- Connection 保持 `Connected`；
- 对远端资源的创建、读取、删除、watch 以及 SPDY exec/attach/port-forward 均成功。

## 9. Fabric 和应用数据面验收

ZBT 不替代 Liqo 数据面。检查 GENEVE 后，用实际应用验证：

```bash
kubectl get genevetunnels.networking.liqo.io -A -o wide
kubectl get virtualnodes -o wide
```

部署一个 offload 到远端 VirtualNode 的 UDP echo Server，然后从本地 Pod 发送五个数据报：

```bash
udp-client --mode=client --target=<remote-pod-ip>:9999 \
  --message=liqo-zbt-check --count=5 --timeout=3s
```

验收条件是五个 sequence 均得到内容一致的 echo 回复。还应补充 Service DNS、NetworkPolicy、MTU 大报文、Gateway 切换和长时稳定性测试。

## 10. 回滚

1. 确认 direct API Server 连通后，将所有集群的 `controlPlane.transport` 同时改回 `direct` 并 Helm upgrade。
2. 等待 Identity Secret 重建/更新，确认不再带 Zenoh required 标记。
3. 确认 peering、RemoteResourceSlice 和 VirtualNode 正常后，再删除 ZBT listener/backend 与 `RemoteAPIAccess`。

不要先删除 ZBT listener/backend；在 Secret 仍标记为 Zenoh required 时，这会刻意导致远端 API 访问失败。
