# syntax=docker/dockerfile:1

FROM node:20-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN npm install
COPY web/ ./
RUN npm run build

FROM golang:1.22-alpine AS build
WORKDIR /src
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/dist/. ./cmd/server/webdist/
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /out/k8man ./cmd/server

# alpine：国内构建更稳；root 便于 local-path PVC 写入 SQLite
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata \
  && cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime \
  && echo Asia/Shanghai > /etc/timezone
WORKDIR /
COPY --from=build /out/k8man /k8man
ENV K8MAN_ADDR=:8080
ENV K8MAN_DATA_DIR=/data
ENV K8MAN_DB_PATH=/data/k8man.db
ENV TZ=Asia/Shanghai
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/k8man"]
