# 仅打包已编译的二进制（请先在宿主机/容器内生成 ./k8man）
# 避免在 2C2G 构建机上于 BuildKit 多阶段内编译 client-go
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
