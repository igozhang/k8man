# API

统一响应：`{ "code": 0, "message": "ok", "data": ... }`。`code != 0` 表示失败。

写操作统一挂载 `AuthWrite` 中间件（首版透传），路径前缀 `/api/v1/write`。只读前缀 `/api/v1/read`。

## Health

- `GET /api/health`

## UI

- `GET /api/v1/read/ui` — 页眉品牌名（`title`）、副标题（`tagline`）、是否有 banner（读 `etc/k8man.conf`）
- `GET /banner` — 头部 banner 图片（`etc` 下有图则返回，否则 404）

## 只读 `/api/v1/read`

### 集群

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/clusters` | 集群列表（不含 kubeconfig 原文） |
| GET | `/clusters/:id` | 集群详情 |
| POST | `/clusters/:id/test` | 连通性与权限重探测（不改库） |
| GET | `/clusters/:id/namespaces` | Namespace 列表 |
| GET | `/clusters/:id/nodes` | 节点列表（project 返回 403） |

### 工作负载

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/clusters/:id/namespaces/:ns/pods` | Pod 列表 |
| GET | `/clusters/:id/namespaces/:ns/pods/:name` | Pod 详情 |
| GET | `/clusters/:id/namespaces/:ns/pods/:name/logs?tail=200` | Pod 日志（默认尾部 200 行、上限 500；服务端 LimitBytes 256KiB） |
| GET | `/clusters/:id/namespaces/:ns/deployments` | Deployment 列表 |
| GET | `/clusters/:id/namespaces/:ns/statefulsets` | StatefulSet 列表 |
| GET | `/clusters/:id/namespaces/:ns/daemonsets` | DaemonSet 列表 |
| GET | `/clusters/:id/namespaces/:ns/services` | Service 列表 |
| GET | `/clusters/:id/namespaces/:ns/configmaps` | ConfigMap 列表 |
| GET | `/clusters/:id/namespaces/:ns/configmaps/:name` | ConfigMap 详情（data） |
| GET | `/clusters/:id/namespaces/:ns/workloads/:kind/:name/spec-edit` | 控制器常用字段摘要 |
| GET | `/clusters/:id/namespaces/:ns/resources/:kind/:name/yaml` | YAML 查看 |

`kind` YAML 白名单：`pod` / `deployment` / `statefulset` / `daemonset` / `service` / `configmap` / `namespace`（及复数形式）。

`spec-edit` 的 `kind`：`deployment` / `statefulset` / `daemonset`。

### 监控

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/clusters/:id/metrics/nodes` | 节点指标 |
| GET | `/clusters/:id/metrics/pods?namespace=` | Pod 指标 |

指标接口错误码（前端按码降级提示）：

| code | 含义 |
|------|------|
| 403 | 权限不足（含 project 调节点指标） |
| 404 | 未安装 metrics-server 或指标 API 不可用 |
| 502 | 其它不可用（超时、连接失败等） |

`project` kubeconfig 调用节点指标返回 403；Pod 指标仍可按命名空间读取（在有权时）。

### 文档

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/docs?category=&q=` | 文档列表 |
| GET | `/docs/:docId` | 文档详情 |

### Hosts

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/hosts` | 私有解析列表（`{path,items}`） |

## 写操作 `/api/v1/write`

### 集群

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/clusters` | 导入 kubeconfig body: `{name,description,kubeconfig}` |
| PUT | `/clusters/:id` | 更新（可只改 name/description，或同时更新 kubeconfig） |
| DELETE | `/clusters/:id` | 删除 |

### 运维

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/clusters/:id/namespaces/:ns/pods/:name/restart` | 删除 Pod 触发重建 |
| PATCH | `/clusters/:id/namespaces/:ns/deployments/:name/scale` | body: `{replicas}` |
| PATCH | `/clusters/:id/namespaces/:ns/statefulsets/:name/scale` | body: `{replicas}` |
| PUT | `/clusters/:id/namespaces/:ns/resources/:kind/:name/yaml` | body: `{yaml}` |
| PATCH | `/clusters/:id/namespaces/:ns/configmaps/:name/data` | body: `{data}` 替换明文 data |
| PATCH | `/clusters/:id/namespaces/:ns/workloads/:kind/:name/spec-edit` | 常用字段 merge 更新 |

`spec-edit` body 示例：

```json
{
  "container": "app",
  "image": "registry/app:1.2.3",
  "env": [{ "name": "FOO", "value": "bar" }],
  "envFrom": [{ "type": "configMapRef", "name": "app-config" }],
  "resources": {
    "requestsCpu": "100m",
    "requestsMemory": "256Mi",
    "limitsCpu": "500m",
    "limitsMemory": "512Mi"
  }
}
```

带 `valueFrom` 的环境变量仅可保留/删除，不可在此新建或改引用目标。

### 文档

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/docs` | 新建 |
| PUT | `/docs/:docId` | 更新 |
| DELETE | `/docs/:docId` | 删除 |

文档 body：`{title,slug,category,tags,summary,content,pinned}`。

### Hosts

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/hosts` | 添加 |
| PUT | `/hosts/:hostId` | 更新 |
| DELETE | `/hosts/:hostId` | 删除 |
