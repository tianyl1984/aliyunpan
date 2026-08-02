# aliyunpan Web 管理界面的容器镜像。
#
# 构建上下文必须是仓库根目录（需要 Go 源码和 web/ 前端源码），例如：
#   docker build -f docker/webui/Dockerfile -t aliyunpan-webui:local .
# 或直接用同目录的 docker-compose.yml（已配好 context 与 dockerfile）。
#
# 前端产物不在 git 里（仓库只有 assets/dist/.gitkeep 占位），所以这里
# 必须自己跑一次 vite 构建，不能指望上下文里带着 dist。
#
# 基础镜像和 npm 源都做成了构建参数：国内环境连不上 Docker Hub / npmjs 时，
# 可以整体换成镜像站，不必改 Dockerfile。例如：
#   docker build -f docker/webui/Dockerfile \
#     --build-arg NODE_IMAGE=<镜像站>/node:20-alpine \
#     --build-arg GO_IMAGE=<镜像站>/golang:1.24-alpine \
#     --build-arg RUNTIME_IMAGE=<镜像站>/alpine:3.21 \
#     --build-arg NPM_REGISTRY=https://registry.npmmirror.com \
#     --build-arg GOPROXY=https://goproxy.cn,direct \
#     -t aliyunpan-webui:local .

ARG NODE_IMAGE=node:24.6.0-alpine3.21
ARG GO_IMAGE=golang:1.24-alpine
ARG RUNTIME_IMAGE=alpine:3.21

# ---------- 阶段 1：构建前端 ----------
FROM ${NODE_IMAGE} AS webbuilder

ARG NPM_REGISTRY

WORKDIR /src/web

# 先只复制依赖清单，让 npm ci 的层能被缓存：
# 只改 Vue 源码时不会重新装依赖。
COPY web/package.json web/package-lock.json ./
RUN if [ -n "$NPM_REGISTRY" ]; then npm config set registry "$NPM_REGISTRY"; fi \
    && npm ci

COPY web/ ./

# vite.config.js 里 build.outDir = '../internal/webui/assets/dist'，
# 所以产物落在 /src/internal/webui/assets/dist
RUN npm run build


# ---------- 阶段 2：编译 Go ----------
FROM ${GO_IMAGE} AS gobuilder

ARG VERSION=docker
ARG TARGETOS
ARG TARGETARCH
ARG GOPROXY

WORKDIR /src

COPY go.mod go.sum ./
RUN if [ -n "$GOPROXY" ]; then go env -w GOPROXY="$GOPROXY"; fi \
    && go mod download

COPY . .

# 用真实的前端产物覆盖仓库里的 .gitkeep 占位目录。
# 必须在 go build 之前，//go:embed 是编译期读取的。
COPY --from=webbuilder /src/internal/webui/assets/dist ./internal/webui/assets/dist

# 校验：产物缺失时立刻失败，避免产出一个"能启动但网页空白"的镜像
RUN test -f ./internal/webui/assets/dist/index.html \
    || (echo "错误: 前端产物缺失，index.html 不存在" >&2 && exit 1)

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-X main.Version=${VERSION} -s -w" -o /out/aliyunpan .


# ---------- 阶段 3：运行时 ----------
FROM ${RUNTIME_IMAGE}

LABEL org.opencontainers.image.title="aliyunpan-webui" \
      org.opencontainers.image.description="Web UI for aliyunpan" \
      org.opencontainers.image.source="https://github.com/tickstep/aliyunpan"

# ca-certificates: 访问阿里云盘 HTTPS 接口必需
# tzdata: 支持 TZ 环境变量，否则容器内时间恒为 UTC
RUN apk add --no-cache ca-certificates tzdata

ENV TZ=Asia/Shanghai \
    ALIYUNPAN_DOCKER=1 \
    ALIYUNPAN_CONFIG_DIR=/config \
    ALIYUNPAN_WEBUI_PORT=8080 \
    ALIYUNPAN_WEBUI_LOCAL_ROOTS=/data

# /config 存放 aliyunpan_config.json(登录凭据)、webui.json、下载断点、
#         以及浏览器直传的暂存文件 webui_stage/，必须持久化
# /data   网页里可浏览/下载到的本地目录白名单
RUN mkdir -p /config /data
VOLUME ["/config", "/data"]

WORKDIR /app
COPY --from=gobuilder /out/aliyunpan /app/aliyunpan
COPY docker/webui/entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/aliyunpan /app/entrypoint.sh

EXPOSE 8080

# /api/auth/status 是公开的 GET 接口，不需要会话，正好用来探活
HEALTHCHECK --start-period=10s --interval=30s --timeout=5s --retries=3 \
    CMD wget -q -O /dev/null "http://127.0.0.1:${ALIYUNPAN_WEBUI_PORT}/api/auth/status" || exit 1

# 用 exec 形式，保证 PID 1 是 entrypoint.sh，SIGTERM 能传到 aliyunpan 进程，
# 触发它的优雅退出（先落盘传输断点，再关 HTTP）
ENTRYPOINT ["/app/entrypoint.sh"]
