#!/bin/sh
# 构建 Web 管理界面的前端资源。
#
# 产物落在 internal/webui/assets/dist，由 go:embed 打进二进制。
# 该产物与 GOOS/GOARCH 无关，所以跨平台交叉编译前跑一次即可。
#
# 注意：dist 产物不提交进 git，仓库里只有 assets/dist/.gitkeep 占位
# （go:embed 在目录不存在或为空时会编译失败，占位文件让 go build 能过）。
# 项目没有 CI，build.sh 直接调用 go build，所以每次打包前都要先跑本脚本，
# 否则编译能过，但二进制里没有前端资源，网页打开是空白。
#
# 用法：
#   ./build_web.sh          # 安装依赖并构建
#   ./build_web.sh --ci     # 使用 npm ci（要求 package-lock.json 存在）

set -e

build_dir=$(dirname "$0")
cd "$build_dir/web"

if ! command -v npm >/dev/null 2>&1; then
  echo "错误: 未找到 npm，请先安装 Node.js (>= 18)" >&2
  exit 1
fi

if [ "$1" = "--ci" ]; then
  npm ci
else
  npm install
fi

npm run build

echo ""
echo "前端资源已构建到 internal/webui/assets/dist"
echo "接下来运行 go build 或 ./build.sh 即可把它打进二进制"
