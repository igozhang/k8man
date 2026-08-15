# 部署

## 镜像构建

在仓库根目录：

```bash
docker build -f deploy/Dockerfile -t k8man:0.1.0 .
```

多阶段构建：先构建 Vue 前端，再编译 Go 静态二进制并嵌入 `webdist`，最终使用 distroless 非 root 镜像。

## 部署到 Kubernetes

```bash
kubectl apply -f deploy/k8s/k8man.yaml
```

资源说明：

- Namespace：`k8man`
- PVC：`k8man-data`（挂载 `/data`，持久化 SQLite）
- Deployment：单副本
- Service：`k8man` ClusterIP，端口 80 → 容器 8080

按需添加 Ingress，并限制仅内网访问（首版无登录鉴权）。

## 环境变量

| 变量 | 默认（容器内） | 说明 |
|------|----------------|------|
| `K8MAN_ADDR` | `:8080` | 监听地址 |
| `K8MAN_DATA_DIR` | `/data` | 数据目录 |
| `K8MAN_DB_PATH` | `/data/k8man.db` | SQLite 文件 |

## 升级注意

- 保持 PVC 不变即可保留集群配置与运维文档
- 仅替换镜像并滚动更新 Deployment
- SQLite 单连接写入，建议副本数保持为 1

## 安全提示

- kubeconfig 明文存于 SQLite，视为敏感数据；配合 NetworkPolicy / 内网 Ingress
- 写操作后续可在 `AuthWrite` 中间件统一加鉴权
- YAML 应用仅允许 pod/deployment/service/namespace
