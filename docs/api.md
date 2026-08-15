# API

统一响应：`{ "code": 0, "message": "ok", "data": ... }`。`code != 0` 表示失败。

写操作统一挂载 `AuthWrite` 中间件（首版透传），路径前缀 `/api/v1/write`。只读前缀 `/api/v1/read`。

## Health

- `GET /api/health`

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
| GET | `/clusters/:id/namespaces/:ns/pods/:name/logs?tail=500` | Pod 日志 |
| GET | `/clusters/:id/namespaces/:ns/deployments` | Deployment 列表 |
| GET | `/clusters/:id/namespaces/:ns/services` | Service 列表 |
| GET | `/clusters/:id/namespaces/:ns/resources/:kind/:name/yaml` | YAML 查看 |

`kind` 白名单：`pod` / `deployment` / `service` / `namespace`（及复数形式）。

### 监控

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/clusters/:id/metrics/nodes` | 节点指标 |
| GET | `/clusters/:id/metrics/pods?namespace=` | Pod 指标 |

无 metrics-server 时返回 502，前端降级提示。

### 文档

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/docs?category=&q=` | 文档列表 |
| GET | `/docs/:docId` | 文档详情 |

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
| PUT | `/clusters/:id/namespaces/:ns/resources/:kind/:name/yaml` | body: `{yaml}` |

### 文档

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/docs` | 新建 |
| PUT | `/docs/:docId` | 更新 |
| DELETE | `/docs/:docId` | 删除 |

文档 body：`{title,slug,category,tags,summary,content,pinned}`。
