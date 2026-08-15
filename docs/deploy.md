# 部署

当前生产环境约定：

| 项 | 值 |
|----|----|
| 镜像仓库 | `krccr.ccs.tencentyun.com/igokorea/k8man` |
| 构建机 | `VM-4-11-ubuntu`（nerdctl + buildkit） |
| 运行集群 | `zabbix68` 单节点（K8s v1.29 + containerd） |
| Namespace | `k8man` |
| 存储类 | `local-path`（默认） |
| 访问域名 | `https://k8man.sunwoda-evb.co`（Ingress → ingress-nginx） |

## 1. 构建并推送镜像（VM-4-11-ubuntu）

源码目录示例：`/data/k8man`。仓库凭证需已 `nerdctl login`。

```bash
cd /data/k8man
git pull

# 方式 A：脚本（推荐）
chmod +x deploy/build-and-push.sh
./deploy/build-and-push.sh          # 自动打时间戳 tag，并推 latest

# 方式 B：手工
TAG=$(date +%Y%m%d-%H%M%S)
IMG=krccr.ccs.tencentyun.com/igokorea/k8man
sudo nerdctl build -f Dockerfile -t ${IMG}:${TAG} -t ${IMG}:latest .
sudo nerdctl push ${IMG}:${TAG}
sudo nerdctl push ${IMG}:latest
```

多阶段构建：Node 打前端 → Go 静态编译并嵌入 `webdist` → Alpine 运行镜像（便于国内拉取与 PVC 写库）。

## 2. 部署到 zabbix68

在能执行 `kubectl` 的环境（如 `igo@zabbix68`）：

```bash
cd ~/gitee/k8man   # 或同步后的仓库路径
git pull

# Namespace 已存在也可重复 apply
kubectl apply -f deploy/k8s/k8man.yaml

# 使用 latest 时强制拉新镜像并重启
kubectl -n k8man rollout restart deploy/k8man
kubectl -n k8man rollout status deploy/k8man
```

若节点无法拉取私有镜像，先创建拉取 Secret（名称按实际修改，并在 Deployment 中启用 `imagePullSecrets`）：

```bash
kubectl -n k8man create secret docker-registry tcr-igokorea \
  --docker-server=krccr.ccs.tencentyun.com \
  --docker-username='<用户名>' \
  --docker-password='<密码>'
```

## 3. 资源说明

`deploy/k8s/k8man.yaml` 包含：

- Namespace：`k8man`
- PVC：`k8man-data`（`local-path`，挂载 `/data`，持久化 SQLite）
- Deployment：单副本，`Recreate`（避免双实例抢同一 PVC）
- Service：ClusterIP `80` → 容器 `8080`
- Ingress：主机名 `k8man.sunwoda-evb.co`，`ingressClassName: nginx`

## 4. 验证

```bash
kubectl -n k8man get pod,svc,ingress,pvc
kubectl -n k8man logs -l app=k8man --tail=100

# 集群内
kubectl -n k8man exec -it deploy/k8man -- wget -qO- http://127.0.0.1:8080/api/health

# 域名（需 DNS 指向 ingress）
curl -sS https://k8man.sunwoda-evb.co/api/health
# 或 http://k8man.sunwoda-evb.co/api/health
```

期望：`{"status":"ok","version":"0.1.0"}`。浏览器打开域名后，在「KubeConfig」导入有权限的 kubeconfig 即可使用。

## 5. 环境变量

| 变量 | 默认（容器内） | 说明 |
|------|----------------|------|
| `K8MAN_ADDR` | `:8080` | 监听地址 |
| `K8MAN_DATA_DIR` | `/data` | 数据目录 |
| `K8MAN_DB_PATH` | `/data/k8man.db` | SQLite 文件 |
| `TZ` | `Asia/Shanghai` | 时区 |

## 6. 升级

1. 在 VM-4-11 重新 build / push（更新 `latest` 或新 TAG）
2. 若清单里写死 TAG，先改 `image:` 再 `kubectl apply`
3. `kubectl -n k8man rollout restart deploy/k8man`
4. **不要删 PVC**，否则集群配置与运维文档会丢失
5. 副本数保持为 1（SQLite）

## 7. 安全提示

- 首版无登录鉴权；域名与 Ingress 宜限制内网或加网关鉴权
- kubeconfig 明文存于 PVC 上的 SQLite，按敏感数据保护
- 写操作后续可在 `AuthWrite` 中间件统一加鉴权
- YAML 应用仅允许 pod / deployment / service / namespace
