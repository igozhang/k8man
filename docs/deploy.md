# 部署

当前生产环境约定：

| 项 | 值 |
|----|----|
| 镜像仓库 | `krccr.ccs.tencentyun.com/igokorea/k8man` |
| 构建机 | `VM-4-11-ubuntu`（nerdctl + buildkit，约 2C2G） |
| 运行集群 | `zabbix68` 单节点 |
| Namespace | `k8man` |
| 域名 | `k8man.sunwoda-evb.co` |

## 为何不能在 Dockerfile 里直接 `go build`

依赖含 `k8s.io/client-go`，在 2C2G 上于 BuildKit 多阶段内编译会长时间占满 CPU/内存（看似卡死）。做法改为：

1. 用限 CPU/内存的容器编译出 `./k8man`
2. 再用极简 Dockerfile 只打包该二进制（秒级）

## 1. Ctrl+C 后先清残留

```bash
sudo killall -9 go node npm 2>/dev/null; sudo systemctl restart buildkit; sleep 2; free -h; uptime
```

## 2. 构建并推送（仓库根目录，一条命令）

同步含新 Dockerfile 的代码后执行（首次 `go build` 仍可能需十余分钟，但可在 `nerdctl run` 日志里看到编译进度；第二次起有 volume 缓存会快很多）：

```bash
TAG=$(date +%Y%m%d-%H%M%S) && echo "TAG=${TAG}" && IMG=krccr.ccs.tencentyun.com/igokorea/k8man && nerdctl run --rm --cpus 1 --memory 1536m -v "$PWD/web:/web" -w /web -v k8man-npm:/root/.npm -e NODE_OPTIONS=--max-old-space-size=384 node:20-alpine sh -ec 'npm install --no-audit --no-fund && npm run build' && rm -rf cmd/server/webdist && mkdir -p cmd/server/webdist && cp -a web/dist/. cmd/server/webdist/ && nerdctl run --rm --cpus 1 --memory 1536m -v "$PWD:/src" -w /src -v k8man-gomod:/go/pkg/mod -v k8man-gocache:/root/.cache/go-build -e CGO_ENABLED=0 -e GOMAXPROCS=1 -e GOMODCACHE=/go/pkg/mod -e GOCACHE=/root/.cache/go-build golang:1.22-alpine sh -ec 'apk add --no-cache git && go build -p=1 -ldflags="-s -w" -o /src/k8man ./cmd/server' && BUILDKIT_MAX_PARALLELISM=1 nerdctl build --progress=plain -t "$IMG:$TAG" -t "$IMG:latest" . && nerdctl push "$IMG:$TAG" && nerdctl push "$IMG:latest"
```

## 3. 部署到 zabbix68

```bash
cd ~/gitee/k8man && git pull && kubectl apply -f deploy/k8s/k8man.yaml && kubectl -n k8man rollout restart deploy/k8man && kubectl -n k8man rollout status deploy/k8man && kubectl -n k8man get pod,svc,ingress,pvc
```

## 4. 验证

```bash
kubectl -n k8man logs -l app=k8man --tail=100
curl -sS http://k8man.sunwoda-evb.co/api/health
```

## 5. 资源与环境变量

- PVC：`k8man-data`（`local-path` → `/data`），Deployment 单副本 `Recreate`
- `K8MAN_ADDR` / `K8MAN_DATA_DIR` / `K8MAN_DB_PATH` / `TZ`

## 6. 升级与安全

- 保留 PVC；无登录鉴权，域名宜内网；kubeconfig 存 SQLite 按敏感数据保护
