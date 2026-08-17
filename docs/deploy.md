# 部署

| 项 | 值 |
|----|----|
| 镜像仓库 | `krccr.ccs.tencentyun.com/igokorea/k8man` |
| 构建机 | `VM-4-11-ubuntu`（nerdctl + buildkit，约 2C2G） |
| 运行集群 | `zabbix68`，Namespace `k8man` |
| 域名 | `k8man.sunwoda-evb.com` |

## 构建并推送（VM-4-11，仓库根目录）

```bash
systemctl is-active buildkit || systemctl start buildkit
```

```bash
TAG=$(date +%Y%m%d-%H%M%S) && echo "TAG=${TAG}" && BUILDKIT_MAX_PARALLELISM=1 nerdctl build --progress=plain -t "krccr.ccs.tencentyun.com/igokorea/k8man:${TAG}" -t "krccr.ccs.tencentyun.com/igokorea/k8man:latest" . && nerdctl push "krccr.ccs.tencentyun.com/igokorea/k8man:${TAG}" && nerdctl push "krccr.ccs.tencentyun.com/igokorea/k8man:latest"
```

前端在镜像构建阶段嵌入，不写回当前 git 目录。

## 部署（zabbix68）

```bash
kubectl apply -f deploy/k8s/k8man.yaml && kubectl -n k8man rollout restart deploy/k8man && kubectl -n k8man rollout status deploy/k8man && kubectl -n k8man get pod,svc,ingress,pvc
```

## 验证

```bash
curl -sS http://k8man.sunwoda-evb.com/api/health
```

## 说明

- PVC：`local-path` → `/data`（库文件、`/data/etc/hosts`、`k8man.conf`、banner 均在此，重启不丢）
- Hosts：文件 `{K8MAN_ETC_DIR}/hosts`，集群部署默认为 `/data/etc/hosts`；格式 `IP hostname`；页面与直接改文件均可，约 2 秒热加载
- 环境变量：`K8MAN_ADDR` / `K8MAN_DATA_DIR` / `K8MAN_DB_PATH` / `K8MAN_ETC_DIR` / `TZ`
- 无登录鉴权；保留 PVC 升级
