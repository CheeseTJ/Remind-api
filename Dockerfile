# ---- build ----
# SQLite 走 modernc.org 纯 Go 驱动，CGO_ENABLED=0 即可静态编译出单二进制
FROM golang:1.26-alpine AS build
WORKDIR /src
# 国内网络兜底：模块代理加速
ENV GOPROXY=https://goproxy.cn,direct
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/remind ./cmd/remind

# ---- runner ----
FROM alpine:3.20
WORKDIR /data
COPY --from=build /out/remind /usr/local/bin/remind
# 时区数据已由 time/tzdata 内嵌进二进制，alpine 无需再装 tzdata
EXPOSE 8085
ENTRYPOINT ["remind"]
CMD ["-config", "/data/config.yaml"]
