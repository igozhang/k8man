# API

统一响应：`{ "code": 0, "message": "ok", "data": ... }`。`code != 0` 表示失败。

写操作统一挂载 `AuthWrite` 中间件：须在 `/ops` 用当前 igokey 验证，请求带 `X-Igo-Token`（或 `?token=`）。只读前缀 `/api/v1/read` 无需 token。默认只读。

## Health

- `GET /api/health` — 存活/就绪探针；会 `Ping` SQLite。正常 `{ "status": "ok", "version": "..." }`；数据库不可用时 HTTP **503**，`{ "status": "error", "version": "...", "error": "database" }`

## UI

- `GET /api/v1/read/ui` — 页眉品牌名（`title`）、slogan（`tagline`）、是否有自定义 icon / banner（读 `etc/k8man.conf`）
- `GET /icon`、`GET /favicon.ico` — 站点图标（`etc` 下 `icon.jpg` / `icon.png` 优先，否则返回内置马里奥风格数字 8）
- `GET /banner` — 头部 banner 图片（`etc` 下有图则返回，否则 404）

## 只读 `/api/v1/read`

### 集群

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/clusters` | 集群列表（**不含** kubeconfig 原文） |
| GET | `/clusters/:id` | 集群详情；**含** kubeconfig 原文仅当请求带有效写 token，否则省略（供编辑回填） |
| GET | `/clusters/:id/namespaces` | Namespace 列表（List 首页 `resourceVersion=0` 走 API Server watch cache） |
| GET | `/clusters/:id/nodes` | 节点列表（project 返回 403；同样走 watch cache） |
| GET | `/clusters/:id/diagnose?namespace=` | 诊断扫描，只返回异常项（见下方） |
| GET | `/inspections` | 全部集群最新巡检摘要（见「巡检」） |
| GET | `/inspections/:clusterId` | 该集群本轮完整巡检报告 |

集群对象常用字段：

| 字段 | 说明 |
|------|------|
| `permType` | `cluster` / `project` / `unknown` |
| `namespaces` | 有权命名空间 |
| `server` | API Server URL |
| `status` | `ok` / `error` / `unknown`（导入后默认 `unknown`，需点测试） |
| `lastError` | 最近一次测试失败信息 |
| `frontProxy` | 入口探测结果，见下方；空表示未测或无法判断 |
| `insecureSkipTLS` | 是否跳过证书校验（由 kubeconfig 推导，非独立存储列） |

#### `frontProxy`（入口探测）

`POST /clusters/probe` 与 `POST /clusters/:id/test` 在探测 API 的同时，对 kubeconfig **host 的 origin**（不是 `/k8s/clusters/...` 等 API path）并行 GET `/ping` 和 `/k8man-front-probe`（约 2 秒超时，**等两次都返回**）。不跟随跳转，以免 302 进 Rancher 页面后误标。看的是 **最前面那一层**：未知路径的 nginx / ingress-nginx 指纹优先于 `/ping=pong`。外置 nginx 把 `/ping` 和 `/k8s/...` 反代到 Rancher、其它路径仍是 nginx 404/302 时，标为 `nginx`（外置 nginx）。启发式分类，不确定则空：

| 值 | 页面标签 | 典型依据 |
|------|------|------|
| `ingress-nginx` | ingress-nginx | 未知路径正文含 `default backend`（ingress-nginx 默认后端） |
| `nginx` | 外置 nginx | HTML 404 / Welcome to nginx / `Server: nginx` 的 404 或 302（**不含** default backend 字样） |
| `rancher` | Rancher | `/ping` 返回 `pong`，且未知路径也不是 nginx/ingress-nginx（流量直接进 Rancher） |
| `apiserver` | （不展示） | JSON `Status` |
| 空 | （不展示） | 不确定 |

改 kubeconfig / API Server / TLS 选项后该字段清空，需再测。已保存的旧结果需再点「测试」才会按新规则更新。

### 诊断

`GET /clusters/:id/diagnose?namespace=`

并行扫描节点（project 跳过）、Deployment / StatefulSet / DaemonSet、Pod、近期 `type=Warning` 事件。`namespace` 为空则优先集群级 List（首页 `resourceVersion=0` 走 API Server watch cache）；仅 403 时才回退按命名空间拉取。某一类失败不阻断其它类，写入 `errors`。

为降低对 API Server / etcd 的压力：Pod 带 `status.phase!=Succeeded` 过滤已完成 Job；事件最多 80 条；指标单独 5s 超时，失败则跳过用量告警。Warning 事件优先挂到已发现的异常 `evidence` 上，未匹配的才作为独立 `event` 行。

响应 `data`：

```json
{
  "issues": [
    {
      "severity": "critical",
      "category": "pod",
      "kind": "Pod",
      "namespace": "default",
      "name": "web-0",
      "reason": "CrashLoopBackOff",
      "message": "back-off restarting failed container",
      "age": "12m",
      "since": "1m",
      "node": "n1",
      "nodeIP": "10.0.0.8",
      "container": "app",
      "evidence": [
        "restart[1] 1m ago pod=web-0 container=app OOMKilled exit=137 · status.lastState",
        "restart[2] 8m ago pod=web-0 container=app BackOff ×4 · event",
        "restart[3] 12m ago pod=web-0 container=app Killing · event",
        "phase=Running ready=0/1 restarts=12",
        "node=n1 ip=10.0.0.8",
        "app waiting CrashLoopBackOff restarts=12: back-off | lastExit OOMKilled exit=137 1m ago"
      ],
      "logPath": "/var/log/pods/default_web-0_<uid>/app/11.log",
      "logSource": "previous container=app tail=40",
      "logExcerpt": [
        "ERROR connection refused",
        "retrying",
        "FATAL panic: nil pointer"
      ]
    }
  ],
  "counts": { "nodes": 0, "workloads": 0, "pods": 1, "events": 0, "critical": 1, "warning": 0 },
  "scan": { "nodes": 12, "pods": 80, "deployments": 20, "statefulSets": 2, "daemonSets": 3, "events": 40, "eventsAttached": 12, "logsFetched": 1, "elapsedMs": 900, "fromCache": true },
  "truncated": false,
  "skippedNodes": false
}
```

- `severity`：`critical` / `warning`。`Evicted` / `Shutdown` 等已被 kubelet 清掉的 Failed 残留不列入结果（节点恢复后无需处理）
- `category`：`node` / `workload` / `pod` / `event`
- `node` / `nodeIP`：所在节点名与节点 IP。Pod 来自 `spec.nodeName` / `status.hostIP`；Warning 事件优先取 `Event.source.host`，再用已扫描 Pod/Node 补 IP
- `reporter` / `count`：事件上报组件（kubelet 等）与发生次数
- `since`：异常条件持续时长（如 Ready=False 的 LastTransitionTime）；`age` 仍为对象创建时长
- `logPath`：节点上 kubelet 日志文件路径（`/var/log/pods/<ns>_<pod>_<uid>/<container>/<n>.log`）
- `logSource`：`previous`/`current`、容器名、tail 行数；CrashLoop/OOM 优先上一份日志
- `logExcerpt`：关键行 ±1 行有限上下文（最多 8 行）；排序后优先 CrashLoop/OOM，最多 8 个严重 Pod；ImagePull / Evicted 等无容器日志的不拉，接口失败也不把错误写进摘录
- `scan`：本轮实际列出的对象数、耗时；`logsFetched` 为实际拉到的日志摘录份数；`fromCache=true` 表示 List 首页走 watch cache；`metricsSkipped` 表示节点指标未采集（用量异常无法判断）
- Warning 事件先最多取 500 条再按时间留 80 条，并优先挂到已发现异常的 `evidence`
- `evidence` 前部可含 `restart[1..3]`：尽力列出最近三次重启时间与原因。`status.lastState` 只保留上次退出；更早次数来自 Warning 的 Killing / BackOff（常被聚合成一条）。凑不满 3 条时带 `restartNote`。控制器行另可有 `selector=`，用于对照下属 Pod
- 控制器副本已 Ready、但下属 Pod 近 2h 仍 CrashLoop / OOM / 累计重启 ≥ 3 时，会多一行 `category=workload` `reason=Restarts`；已有 NotReady 则只挂证据、不另占行。滚动宽限期内不单独占控制器 Restarts 行
- `skippedNodes`：`project` kubeconfig 为 `true`
- `truncated`：扫描或结果达到上限（每类最多 200 条，事件最多 80 条）
- `errors`：可选，某类扫描失败信息（如 `{"pods":"..."}`）

节点 CPU/内存 ≥90% 仅在 metrics-server 可用时计入；指标不可用则跳过，不报错。

### 工作负载

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/clusters/:id/namespaces/:ns/pods` | Pod 列表 |
| GET | `/clusters/:id/namespaces/:ns/pods/:name` | Pod 详情 |
| GET | `/clusters/:id/namespaces/:ns/pods/:name/logs?tail=200&container=&previous=` | Pod 日志（默认尾部 200 行、上限 500；`container` 可选；`previous=1` 读上一份；服务端 LimitBytes 256KiB） |
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

### 巡检

进程内后台对每个已保存 kubeconfig **独立计时**：约 **2 小时** 一轮（完成后再加 ±6 分钟抖动），**串行**执行（同时只跑 1 个，间隔约 15 秒），避免同时打满 API Server。距上次完成不足约 1 小时 50 分则跳过。从未巡检的配置启动后按 id 哈希顺序补跑。周期写死在进程内，**不是** `k8man.conf` 配置项（说明见 conf 尾部注释）。

每轮复用现有逻辑：先 `Probe`（与「测试」相同，并回写 `status` / `permType` / `namespaces` / `server` / `lastError` / `frontProxy`）；失败则记 kubeconfig 无效，不再跑诊断。成功则跑 `Diagnose`（节点/负载/Pod/事件，含节点 CPU/内存 ≥90%；project 跳过节点）。每集群只保留最近一轮。进程退出或请求取消时不落库。改 kubeconfig / API Server / TLS 会删除该集群巡检记录，按尚未巡检重新排队。

监控页底部按集群分块展示异常摘要（每块最多 3 条预览，带缓存时间；点击块打开抽屉）。「一键巡检」与「详情」对路径指定集群执行 `POST /clusters/:id/inspect`（`force`，先可展示 `GET /inspections/:id` 缓存，再实时 Probe + 诊断并更新）。

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/inspections` | 全部集群最新巡检摘要（含尚未巡检的配置） |
| GET | `/inspections/:clusterId` | 该集群本轮完整报告（尚未巡检为 404） |

`GET /inspections` 的 `data`：

```json
{
  "intervalSeconds": 7200,
  "items": [
    {
      "clusterId": "...",
      "clusterName": "prod",
      "checkedAt": "2026-08-20T00:00:00Z",
      "nextDue": "2026-08-20T02:03:00Z",
      "elapsedMs": 900,
      "probeStatus": "ok",
      "critical": 1,
      "warning": 2,
      "summary": "1 严重 / 2 警告",
      "preview": [
        {
          "severity": "critical",
          "category": "pod",
          "kind": "Pod",
          "namespace": "default",
          "name": "web-0",
          "reason": "CrashLoopBackOff",
          "message": "back-off restarting"
        }
      ]
    }
  ]
}
```

`preview` 为该集群按严重度排序后的最多 8 条。详情接口另含 `probe`、`diagnose`（完整诊断报告）与 `issues`（本轮全部异常；kubeconfig 失败时为一条 `category=kubeconfig`）。

### 文档

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/docs?category=&q=` | 文档列表 |
| GET | `/docs/:docId` | 文档详情 |

### Hosts

页面「KubeConfig」底部与本接口共用同一文件（部署时 `/data/etc/hosts`）。格式与系统 hosts 相同：`IP hostname`。

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/hosts` | `{path, items}`；`items` 为 `{id,hostname,ip}` |

## 鉴权 `/api/v1/auth`

方案与站点 igokey 相同：8 位字母、约 1 小时有效；剩余 **>15 分钟** 刷新复用，不足 15 分钟或 `renew=1` 签发新密钥并作废旧密钥与已登录会话。活跃密钥落在 `<data_dir>/igokey-active.json`。同一 IP 1 分钟内登录失败 5 次锁定 1 分钟。

页面：`/igokey` 取钥，`/ops` 输入 key。token **仅存浏览器内存**，刷新页面须重新验证。写请求头 `X-Igo-Token`。

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/igokey?renew=` | 签发或复用当前密钥，`data`: `{key, expiresAt, expiresInSec}` |
| POST | `/login` | body `{key}`；成功 `data`: `{token, expiresAt, expiresInSec}`；错误 401；过频 429 |
| POST | `/session` | body `{token}`；成功 `data`: `{expiresInSec}`；失效 401 |

写接口未带有效 token 时 HTTP **401**，`message` 为「未认证，请先验证 key」/「旧 key 不可用…」/「key 已过期…」。

## 写操作 `/api/v1/write`

### 集群

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/clusters` | 导入。不自动探测，`status` 为 `unknown` |
| POST | `/clusters/probe` | **不落库**，测当前编辑内容（抽屉里「测试」） |
| PUT | `/clusters/:id` | 更新；改 kubeconfig / server / TLS 会清空探测结果、丢弃该集群的客户端缓存，并删除其巡检快照 |
| DELETE | `/clusters/:id` | 删除（同时删除该集群巡检记录） |
| POST | `/clusters/:id/test` | 连通、权限与入口重探测，并回写 `status` / `permType` / `namespaces` / `server` / `lastError` / `frontProxy` |
| POST | `/clusters/:id/inspect` | 对**该**集群立即巡检（Probe + 诊断），重置其 2 小时计时；与后台巡检串行，超时约 90 秒 |

导入 body：`{name, description, kubeconfig, serverOverride?, insecureSkipTLS?}`。`name` 可空，取 kubeconfig 的 `current-context`。`serverOverride` 会改写 kubeconfig 内 cluster.server。

更新 body（字段均可省略）：`{name, description, kubeconfig, serverOverride, insecureSkipTLS}`。`kubeconfig` 不传则不覆盖原文。

`probe` / `test` 的 `data`：`{server, permType, namespaces, status, lastError, frontProxy}`。`probe` 的请求 body 与导入相同（`kubeconfig` 必填）。

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
| POST | `/hosts` | 添加 body: `{hostname, ip}` |
| PUT | `/hosts/:hostId` | 更新；`hostId` 可为 id 或 hostname；body: `{hostname?, ip?}` |
| DELETE | `/hosts/:hostId` | 删除 |

写入后约 2 秒内对后续「测试」与 API 请求生效（进程内解析器热加载）。
