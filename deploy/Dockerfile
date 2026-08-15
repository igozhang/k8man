# 与仓库根目录 Dockerfile 一致：仅打包预编译二进制 ./k8man
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata \
  && cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime \
  && echo Asia/Shanghai > /etc/timezone
COPY k8man /k8man
ENV K8MAN_ADDR=:8080 \
    K8MAN_DATA_DIR=/data \
    K8MAN_DB_PATH=/data/k8man.db \
    TZ=Asia/Shanghai
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/k8man"]
