# Sub2API Guardian —— 一条命令完成前端构建 + 后端编译。
#
# make build         构建当前平台的单二进制（内嵌前端）
# make build-linux   交叉编译 Linux amd64 + arm64
# make dev-*         分别启动前后端开发服务
# make test          后端测试 + 前端类型检查

SHELL := /bin/sh
FRONTEND := frontend
BACKEND := backend
BINARY := guardian
DIST := dist

ifeq ($(OS),Windows_NT)
BINARY := guardian.exe
endif

.PHONY: build build-frontend build-backend build-linux build-linux-amd64 build-linux-arm64 build-checksums \
	dev-backend dev-frontend test test-backend test-frontend fmt clean

build: build-frontend build-backend

build-frontend:
	cd $(FRONTEND) && pnpm install --frozen-lockfile && pnpm build

build-backend:
	cd $(BACKEND) && go build -o $(BINARY) ./cmd/guardian
	@echo "已生成 $(BACKEND)/$(BINARY)，直接运行即可在同一端口提供 API 与面板"

# 交叉编译 Linux。
#
# CGO_ENABLED=0 是关键，也是本项目能无痛交叉编译的原因：
# SQLite 用的是 modernc.org/sqlite（纯 Go 实现），不像 mattn/go-sqlite3 那样需要
# C 编译器和目标平台的工具链。产物是静态链接的单文件，扔到服务器上直接跑，
# 不依赖 glibc 版本，也能塞进 scratch/alpine 镜像。
#
# 前端要先构建：go:embed 会把 dist 打进二进制，不构建就只有占位页。
build-linux: build-frontend build-linux-amd64 build-linux-arm64 build-checksums
	@echo "产物与 checksums.txt 在 $(DIST)/：上传到 GitHub Release 后可供 install.sh 自动安装"

build-linux-amd64:
	@mkdir -p $(DIST)
	cd $(BACKEND) && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
		go build -trimpath -ldflags="-s -w" -o ../$(DIST)/guardian-linux-amd64 ./cmd/guardian

build-linux-arm64:
	@mkdir -p $(DIST)
	cd $(BACKEND) && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
		go build -trimpath -ldflags="-s -w" -o ../$(DIST)/guardian-linux-arm64 ./cmd/guardian

build-checksums:
	cd $(DIST) && sha256sum guardian-linux-amd64 guardian-linux-arm64 > checksums.txt

dev-backend:
	cd $(BACKEND) && go run ./cmd/guardian

dev-frontend:
	cd $(FRONTEND) && pnpm dev

test: test-backend test-frontend

test-backend:
	cd $(BACKEND) && go vet ./... && go test ./...

test-frontend:
	cd $(FRONTEND) && pnpm typecheck

fmt:
	cd $(BACKEND) && gofmt -w .

clean:
	rm -rf $(BACKEND)/$(BINARY) $(DIST) \
		$(BACKEND)/internal/web/dist/assets $(BACKEND)/internal/web/dist/index.html
