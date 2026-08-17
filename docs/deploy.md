# 部署

| 项 | 值 |
|----|----|
| 镜像仓库 | `krccr.ccs.tencentyun.com/igokorea/k8man` |
| 构建机 | `VM-4-11-ubuntu`（nerdctl + buildkit，约 2C2G） |
| 运行集群 | `zabbix68`，Namespace `k8man` |
| 域名 | `k8man.sunwoda-evb.com` |

## 构建并推送（VM-4-11，仓库根目录）

先确保 buildkit 正常：

```bash
systemctl is-active buildkit || systemctl start buildkit
```

再执行（风格对齐 igozhang）：

```bash
TAG=$(date +%Y%m%d-%H%M%S) && echo "TAG=${TAG}" && BUILDKIT_MAX_PARALLELISM=1 nerdctl build --progress=plain -t "krccr.ccs.tencentyun.com/igokorea/k8man:${TAG}" -t "krccr.ccs.tencentyun.com/igokorea/k8man:latest" . && nerdctl push "krccr.ccs.tencentyun.com/igokorea/k8man:${TAG}" && nerdctl push "krccr.ccs.tencentyun.com/igokorea/k8man:latest"
```

Dockerfile 已限并行（前端/Go 串行、`GOMAXPROCS=1`、`go build -p=1`、cache mount）。首次 `go build` 在 2C2G 上仍可能较久；无 Swap 时勿并行其它重任务。

## 部署（zabbix68）

```bash
kubectl apply -f deploy/k8s/k8man.yaml && kubectl -n k8man rollout restart deploy/k8man && kubectl -n k8man rollout status deploy/k8man && kubectl -n k8man get pod,svc,ingress,pvc
```

## 验证

```bash
curl -sS http://k8man.sunwoda-evb.com/api/health
```

## 说明

- PVC：`local-path` → `/data`；单副本 `Recreate`
- 环境变量：`K8MAN_ADDR` / `K8MAN_DATA_DIR` / `K8MAN_DB_PATH` / `TZ`
- 无登录鉴权；保留 PVC 升级
