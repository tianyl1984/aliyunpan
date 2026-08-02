#!/bin/sh
# aliyunpan webui 的容器启动脚本。
#
# 把环境变量翻译成 `aliyunpan webui` 的命令行参数。之所以需要这一层，
# 是因为 webui 命令只认命令行参数，不读环境变量。
#
# -f 关闭通配符展开：下面按逗号切分环境变量时用的是不加引号的展开，
# 值里出现 * 或 ? 会被 shell 当成文件名匹配掉（实测 TRUSTED_ORIGINS=* 会
# 变成 /app 下的文件名），必须禁用。脚本本身不依赖通配符。
set -ef

# ---- 访问口令 ----
# 容器必须监听 0.0.0.0，而 RunWebUI 对非回环地址强制要求口令，
# 所以这里没有"不设口令"的选项，缺失就直接失败，而不是启动一个裸奔的服务。
if [ -n "$ALIYUNPAN_WEBUI_PASSWORD_FILE" ] && [ -f "$ALIYUNPAN_WEBUI_PASSWORD_FILE" ]; then
    ALIYUNPAN_WEBUI_PASSWORD=$(cat "$ALIYUNPAN_WEBUI_PASSWORD_FILE")
fi

if [ -z "$ALIYUNPAN_WEBUI_PASSWORD" ]; then
    echo "错误: 必须设置访问口令。" >&2
    echo "  在 docker-compose.yml 同级建一个 .env 文件写入:" >&2
    echo "    ALIYUNPAN_WEBUI_PASSWORD=你的口令" >&2
    echo "  或使用 docker secret，指定 ALIYUNPAN_WEBUI_PASSWORD_FILE=/run/secrets/xxx" >&2
    exit 1
fi

PORT="${ALIYUNPAN_WEBUI_PORT:-8080}"

# ---- 本地目录白名单 ----
# 网页里能浏览到的服务器本地目录。不指定时 webui 会回退到"家目录 + 默认下载目录"，
# 在容器里那是 /root，不是我们想暴露的东西，所以这里总是显式传。
ROOTS="${ALIYUNPAN_WEBUI_LOCAL_ROOTS:-/data}"

set -- webui \
    --host 0.0.0.0 \
    --port "$PORT" \
    --password "$ALIYUNPAN_WEBUI_PASSWORD"

# 逗号分隔，逐个转成 --local-root。
# 目录不存在时 webui 不会报错，但网页里点进去会失败，所以先建出来。
OLD_IFS="$IFS"
IFS=','
for root in $ROOTS; do
    IFS="$OLD_IFS"
    [ -z "$root" ] && continue
    mkdir -p "$root" 2>/dev/null || echo "警告: 无法创建目录 $root，将按只读处理" >&2
    set -- "$@" --local-root "$root"
    IFS=','
done
IFS="$OLD_IFS"

# ---- 可信来源（多个用逗号分隔）----
# 浏览器实际访问用的地址。反向代理、或宿主端口与容器端口不一致时必须配置，
# 否则写操作会被 CSRF 防护拦下，返回 403 Origin 不匹配。
# 例: ALIYUNPAN_WEBUI_TRUSTED_ORIGINS=https://pan.example.com,http://192.168.1.10:9000
if [ -n "$ALIYUNPAN_WEBUI_TRUSTED_ORIGINS" ]; then
    OLD_IFS="$IFS"
    IFS=','
    for origin in $ALIYUNPAN_WEBUI_TRUSTED_ORIGINS; do
        IFS="$OLD_IFS"
        # 容忍配置里写的空格和空项
        origin=$(printf '%s' "$origin" | tr -d '[:space:]')
        [ -z "$origin" ] && { IFS=','; continue; }
        set -- "$@" --trusted-origin "$origin"
        IFS=','
    done
    IFS="$OLD_IFS"
    echo "可信来源: $ALIYUNPAN_WEBUI_TRUSTED_ORIGINS"
fi

# ---- 可选 TLS ----
# 一般建议由反向代理终止 TLS，容器内跑明文即可；这里保留直连 HTTPS 的能力。
if [ -n "$ALIYUNPAN_WEBUI_TLS_CERT" ] && [ -n "$ALIYUNPAN_WEBUI_TLS_KEY" ]; then
    set -- "$@" --tls-cert "$ALIYUNPAN_WEBUI_TLS_CERT" --tls-key "$ALIYUNPAN_WEBUI_TLS_KEY"
fi

# 注意: --allow-shell 在这里故意不做支持。
# webui 只允许监听回环地址时开启它，容器必然监听 0.0.0.0，传了会直接拒绝启动。

echo "启动 aliyunpan webui: 端口 $PORT, 本地目录白名单 $ROOTS"

# exec 替换掉 shell 自身，让 aliyunpan 成为 PID 1 直接收到 SIGTERM，
# 走它自己的优雅退出流程（落盘传输断点 → 关闭 HTTP）
exec /app/aliyunpan "$@"
