# k8s多集群运维工具（k8man）

通过导入有权限的 kubeconfig（含 Rancher 集群管理员 / Project 两类），对目标 K8s 集群做运维、管理与监控。

## 特性

- 多集群 kubeconfig 导入、连通性测试、cluster / project 权限自动探测
- 工作负载查看与运维：Pod 日志/重启、Deployment/StatefulSet 扩缩容、ConfigMap 键值编辑、控制器常用字段（镜像/环境变量/资源）编辑、受限 YAML 查看与应用
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

默认监听 `:8080`，数据库 `./data/k8man.db`，配置目录 `./etc`。

环境变量（优先级高于 `etc/k8man.conf`）：

| 变量 | 默认 | 说明 |
|------|------|------|
| `K8MAN_ADDR` | `:8080` | 监听地址 |
| `K8MAN_DATA_DIR` | `./data` | 数据目录 |
| `K8MAN_DB_PATH` | `$DATA_DIR/k8man.db` | SQLite 路径 |
| `K8MAN_ETC_DIR` | `./etc` | 配置与可选自有 banner 目录（容器内 `/etc/k8man`） |

`etc/k8man.conf`：头部为生效配置，尾部为说明与示例；缺失或无效时启动自动生成。将自有 `banner.jpg`（或 png/webp/gif）放入 `etc` 即启用头部 banner；无文件则默认浅青面板风格（配色/布局参考同类站点，不使用外部 banner 图）。

### 前端（开发）

```bash
cd web
npm install
npm run dev   # Vite 代理 /api -> :8080
```

### 容器 / 生产部署

- 镜像仓库：`krccr.ccs.tencentyun.com/igokorea/k8man`
- 构建机：`VM-4-11-ubuntu`（nerdctl，2C2G 限并行 + cache）
- 运行：`zabbix68`，`k8man` namespace，域名 `k8man.sunwoda-evb.com`

步骤见 [docs/deploy.md](docs/deploy.md)（单条 build/push 命令）。

## 导入 kubeconfig

1. 打开页面「KubeConfig」
2. 粘贴 Rancher「Download KubeConfig‑Cluster」或「Project kubeconfig」
3. 保存后自动探测权限类型：
   - 能 List Node → `cluster`
   - 否则 → `project`（仅其可访问 Namespace）
4. 在页面功能区顶部「选择集群」后进行运维/监控

能力边界完全由导入的 kubeconfig 决定。

## 目录说明

```
cmd/server/          入口与嵌入前端
etc/                 k8man.conf 与可选 banner 图片
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
