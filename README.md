# k8s多集群运维工具（k8man）

通过导入有权限的 kubeconfig（含 Rancher 集群管理员 / Project 两类），对目标 K8s 集群做运维、管理与监控。

## 特性

- 多集群 kubeconfig 导入、连通性测试、cluster / project 权限自动探测
- 工作负载查看与运维：Pod 日志/重启、Deployment 扩缩容、受限 YAML 查看与应用
- 节点与基础监控（依赖目标集群 metrics-server）
- 产品内运维文档（Markdown）
- 内置 SQLite + PVC 持久化，无独立数据库
- API 读写分离：`/api/v1/read` 与 `/api/v1/write`（写组预留 `AuthWrite` 鉴权位）

## 快速开始（本地）

### 后端

```bash
go mod tidy
go run ./cmd/server
```

默认监听 `:8080`，数据库 `./data/k8man.db`。

环境变量：

| 变量 | 默认 | 说明 |
|------|------|------|
| `K8MAN_ADDR` | `:8080` | 监听地址 |
| `K8MAN_DATA_DIR` | `./data` | 数据目录 |
| `K8MAN_DB_PATH` | `$DATA_DIR/k8man.db` | SQLite 路径 |

### 前端（开发）

```bash
cd web
npm install
npm run build
# 将 dist 复制到 cmd/server/webdist 后重新启动后端
# 或：npm run dev  （Vite 代理 /api -> :8080）
```

### 容器 / 生产部署

- 镜像仓库：`krccr.ccs.tencentyun.com/igokorea/k8man`
- 构建机：`VM-4-11-ubuntu`（nerdctl）→ 推送仓库
- 运行：`zabbix68` 单节点，`k8man` namespace，域名 `k8man.sunwoda-evb.co`

详见 [docs/deploy.md](docs/deploy.md)（含 build/push/apply 完整步骤）。

## 导入 kubeconfig

1. 打开页面「KubeConfig」
2. 粘贴 Rancher「Download KubeConfig‑Cluster」或「Project kubeconfig」
3. 保存后自动探测权限类型：
   - 能 List Node → `cluster`
   - 否则 → `project`（仅其可访问 Namespace）
4. 右上角选择当前集群后进行运维/监控

能力边界完全由导入的 kubeconfig 决定。

## 目录说明

```
cmd/server/          入口与嵌入前端
internal/api/read|write  读写 API 分包
internal/kube        client-go 封装
internal/store       SQLite
web/                 Vue3 前端
deploy/              Dockerfile 与 K8s 清单
docs/api.md          API 一览
docs/deploy.md       部署说明
```

读写分离扩展点：在 `internal/api/middleware/auth.go` 的 `AuthWrite` 中实现鉴权即可约束写操作。

## 文档

- [API](docs/api.md)
- [部署](docs/deploy.md)
