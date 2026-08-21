# k8s多集群运维（k8man）

通过导入有权限的 kubeconfig（含 Rancher 集群管理员 / Project 两类），对目标 K8s 集群做运维、管理与监控。

## 特性

- 多集群 kubeconfig 导入 / 编辑（抽屉回填原文）、连通性测试、cluster / project 权限探测
- 轻量入口探测：ingress-nginx / 外置 nginx / Rancher（点「测试」后写在状态列）
- 页面 Hosts（`IP hostname`）：kubeconfig 域名解析不到时用，约 2 秒生效
- 工作负载查看与运维：Pod 日志/重启、Deployment/StatefulSet 扩缩容、ConfigMap 键值编辑、控制器常用字段（镜像/环境变量/资源）编辑、受限 YAML 查看与应用
- 节点与基础监控（依赖目标集群 metrics-server）；后台约 2 小时巡检全部 kubeconfig 有效性与集群异常，监控页底部摘要；可在监控页选择配置后一键巡检（先展示缓存再实时更新）
- 产品内运维文档（Markdown）
- 内置 SQLite + PVC 持久化，无独立数据库
- API 读写分离：`/api/v1/read` 只读；`/api/v1/write` 须在 `/ops` 用 igokey 验证后携带 `X-Igo-Token`

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
| `K8MAN_ETC_DIR` | `./etc` | 配置与可选自有 icon / banner 目录（容器内 `/etc/k8man`） |

`etc/k8man.conf`：头部为生效配置，尾部为说明与示例（含巡检行为）；缺失或无效时启动自动生成。`title` 为页眉左侧品牌名（兼浏览器标签页标题）。站名下方 slogan 固定为 `practice makes perfect`（不可配置；双击复制 igokey）。将自有 `icon.jpg` / `icon.png` 放入 `etc` 即作为 favicon 与页眉图标；无文件则使用内置马里奥风格（红白蓝）数字 8。将自有 `banner.jpg`（或 png/webp/gif）放入 `etc` 即启用头部 banner；无文件则默认浅青面板风格（配色/布局参考同类站点，不使用外部 banner 图）。

### 前端（开发）

```bash
cd web
npm install
npm run dev   # Vite 代理 /api -> :8080
```

### 容器 / 生产部署

- 镜像仓库：`krccr.ccs.tencentyun.com/igokorea/k8man`
- 构建机：`VM-4-11-ubuntu`（nerdctl，2C2G 限并行 + cache）
- 运行：`zabbix68`，`k8man` namespace，域名 `k8man.igozhang.cn`

步骤见 [docs/deploy.md](docs/deploy.md)（单条 build/push 命令）。

## 导入 kubeconfig

1. 打开「KubeConfig」→「导入 kubeconfig」（右侧抽屉）
2. 粘贴或上传 Rancher「Download KubeConfig‑Cluster」或 Project kubeconfig；标题可留空（用 `current-context`）
3. 可选：改 API Server、勾选「跳过 TLS 证书校验」（内网 / 自签证书）
4. 点「测试」探测连通、权限与入口；**保存不会自动探测**，导入后状态为「未测试」
   - 能 List Node → `cluster`，否则 → `project`（仅其可访问 Namespace）
   - 入口（启发式，写在状态列）：看最前面那一层。外置 nginx 反代 Rancher 时显示「外置 nginx」，不会因为 `/ping=pong` 标成 Rancher
5. 域名解析不到时，在同页 Hosts 按系统 hosts 格式添加 `IP hostname`，约 2 秒后再生效，再测
6. 「选用」后到工作负载 / 监控 / 诊断

点「编辑」会打开抽屉并加载已保存的 kubeconfig；未保存关闭会确认。改 YAML 或 API Server 后入口标签会清空，需再测；该配置的巡检快照也会丢掉，由后台重新排队。

能力边界完全由导入的 kubeconfig 决定。详情见 [API](docs/api.md)。

## 巡检

进程内对每个已保存 kubeconfig **独立计时**，约 **2 小时** 一轮（完成后 ±6 分钟抖动），**串行**执行，避免同时打满 API Server。无独立配置项，说明写在 `etc/k8man.conf` 注释里。

每轮先做与页面「测试」相同的 Probe（并回写状态），成功再跑诊断扫描（节点/负载/Pod/事件；project 凭证跳过节点）。进程退出或请求取消时不落库。

「监控」页底部展示全部配置的问题摘要（有严重/错误/警告的集群独立成块，每块最多约 3 行预览；仅有提示时不刷块，摘要写「N 提示」）。**一键巡检**与**详情**均对所选 kubeconfig 执行：先展示缓存快照（若有）并立即触发实时巡检，完成后更新。定级与 Restarts 规则与诊断页相同。

## 目录说明

```
cmd/server/          入口与嵌入前端
etc/                 k8man.conf、可选 icon / banner 图片
internal/api/read|write  读写 API 分包
internal/inspect     后台巡检（2h 错开）
internal/kube        client-go 封装
internal/store       SQLite
web/                 Vue3 前端
deploy/              Dockerfile 与 K8s 清单
docs/api.md          API 一览
docs/deploy.md       部署说明
etc/README.md        etc 目录与内置 conf 说明
```

读写分离：默认只读。访问 `/igokey` 取得 8 位字母密钥（约 1 小时有效），在 `/ops` 验证后本会话可写；写接口校验 `X-Igo-Token`。刷新页面需重新验证。

## 文档

- [API](docs/api.md)
- [部署](docs/deploy.md)
