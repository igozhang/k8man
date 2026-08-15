#!/usr/bin/env bash
# 在 VM-4-11-ubuntu 上构建并推送镜像（需已 nerdctl login 腾讯云 CCR）
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

REGISTRY="krccr.ccs.tencentyun.com/igokorea/k8man"
TAG="${1:-$(date +%Y%m%d-%H%M%S)}"
IMAGE="${REGISTRY}:${TAG}"
LATEST="${REGISTRY}:latest"

echo "==> build ${IMAGE}"
sudo nerdctl build -f Dockerfile -t "${IMAGE}" -t "${LATEST}" .

echo "==> push ${IMAGE}"
sudo nerdctl push "${IMAGE}"
echo "==> push ${LATEST}"
sudo nerdctl push "${LATEST}"

echo "OK image=${IMAGE}"
echo "部署时请将 deploy/k8s/k8man.yaml 中 image 改为上述 TAG，或使用 latest 后执行："
echo "  kubectl -n k8man rollout restart deploy/k8man"
