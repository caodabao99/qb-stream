# 构建阶段：在容器内编译，无需本机安装 Go（在 NAS 上直接 docker build 即可）
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
# Vue 前端静态资源由 static.go 的 go:embed 打进二进制，必须一并拷贝
COPY static ./static
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/qb-stream .

# 运行阶段：最小镜像
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/qb-stream /usr/local/bin/qb-stream
# 容器内必须监听所有网卡，电脑才能通过 NAS IP 访问
ENV QBSTREAM_HOST=0.0.0.0
WORKDIR /config
VOLUME /config
EXPOSE 8888
ENTRYPOINT ["/usr/local/bin/qb-stream"]
CMD ["--config", "/config/qb-stream.json"]
