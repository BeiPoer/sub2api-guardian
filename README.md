<div align="center">

# Sub2API Guardian

**为 [sub2api](https://github.com/Wei-Shaw/sub2api) 打造的渠道调度守护服务**

持续测活与测延迟，用统一的健康分驱动熔断、降级、调权与自动回池，
并保证 **每个分组任何时刻都至少有一个渠道存活**。

单个二进制，内嵌前端，零外部依赖。

</div>

---

## 它解决什么

渠道多了之后，人工盯着哪个挂了、哪个变慢、哪个该多分点流量，是件不可持续的事。
Guardian 把这件事自动化：**你只需为每个分组选择「价格优先 / 速度优先 / 均衡」**，
其余阈值都有默认值，可随时在页面上微调。

| 能力 | 做法 |
|---|---|
| 测活 | 通过 sub2api 的账号测试接口（SSE）主动探测，识别认证失败、余额不足、限流、超时 |
| 测延迟 | 取首个内容事件的时间作为首字延迟（TTFB），统计 P50 / P95 |
| 调度倍率 | 每个渠道一个倍率，越低越优先。API Key 可按运营配置的周期（最低 30 秒）读取上游倍率或立即同步；失败保留最近成功值。**仅供调度系统使用，不写回 sub2api** |
| 健康分 | 长短期加权：短期取最近 N 次（最新一次占固定权重），长期取均值，凭据失效一票否决 |
| 熔断 | 凭据失效立即熔断；实时倍率渠道可单独设置倍率上限并开启超限熔断；错误率与延迟可配置为只降级不熔断 |
| 保底 | 熔断会让分组失去可用渠道时，改为「保底强留」并告警，**分组永不断供** |
| 降级 | 低分渠道压低优先级与负载因子，但不停止调度 |
| 调权 | 按策略把权重预算分配到组内渠道，落成 `priority` 与 `load_factor`，带防抖与冷却 |
| 回池 | 熔断渠道低频探测，健康分回升并持续一段时间后自动恢复原配置并上线 |
| 失效处置 | 反复出现指定错误码（如 401/403）的渠道自动暂停 / 停用 / 删除，**默认关闭** |
| 上游渠道 | 独立管理 Sub2API、New API 与其它站点，查看余额、分组、令牌、订阅并配置邮件/企微告警 |

所有对 sub2api 的写操作都会**先记录基线**（接管前的 `priority` / `load_factor` /
`concurrency` / `rate_multiplier` / `schedulable`），随时可以一键交还控制权。

### 限流（429）永不摘除调度

这是一条不受任何配置影响的硬约束，值得单独说明。

sub2api 自己已经在处理限流：上游返回 429 时它写入 `rate_limit_reset_at`，
选路查询直接排除该账号，**窗口一过自动恢复**。Guardian 若再去改 `schedulable`，
就把「到点自动恢复」换成了「要等恢复探测跑成功才回来」——高并发时这段空窗
意味着可用容量凭空少一截，而那正是最需要容量的时候。

因此限流只压低权重（流量自然挪走），渠道**始终留在池子里**，限流一结束立刻承接流量。
分组状态会显示为「限流中」而不是「健康」，也不会和真故障混为一谈。

---

## 界面

### 总览

![总览](docs/images/overview.png)

> 受管渠道数、平均健康分、已分配并发、风险分组；分组健康矩阵一屏看清哪些组需要处理。

### 分组调度

![分组调度](docs/images/groups.png)

> 每个分组一张卡：策略选择器、保底池、权重分配、组内实时排序。

### 渠道池

![渠道池](docs/images/channels.png)

> 健康分环、最近 10 次结果、首字 P50/P95、调度倍率、权重、优先级与负载的现值→目标值；API Key 渠道可单独同步上游倍率，并配置超阈值熔断。

### 渠道管理

侧栏「渠道管理」（与「系统」同级）包含「渠道汇总」和「渠道列表」：汇总页集中展示余额、
令牌与倍率，列表页在左侧选择渠道、右侧查看详情。该模块使用独立的数据与接口，
不会加入 Guardian 的渠道池调度。支持 Sub2API / New API 同步、余额历史、分组与令牌、
订阅、令牌模型和分组修改，以及低余额、消耗速率、分组新增/移除/倍率变化五类邮件与企微应用告警；
`other` 类型只保存站点记录。每个渠道还可以配置充值比例（默认 1:1，数值表示到账倍数）、支付宝/微信/卡网
充值方式和自定义手续费；充值方式会以图标显示在汇总卡片右上角，三项配置也会在详情中展示。

企微通知参考 [push-server](https://github.com/qingzhou-dev/push-server) 对企业微信 API 的实现，Guardian 直接调用官方接口：
在渠道汇总或详情页打开「企微设置」，填写企业 ID、应用 AgentId、应用 Secret 和企微成员 ID（或 `@all`）。

### 策略配置

![策略配置总览](docs/images/policy-overview.png)

> 运营配置（开关 + 阈值）、系统级规则（健康分公式、事件分值、错误分类）、守护范围。

![熔断与限流策略](docs/images/policy-breaker.png)

### 事件日志

![事件日志](docs/images/events.png)

> 同步、探测、熔断、回池和参数写回都有可筛选的审计记录。

### 连接设置

![连接设置](docs/images/connection.png)

> 查看 sub2api 连接、自动守护、真实流量接入情况以及最近一轮调度摘要。

---

## 快速开始

### Linux 一键安装（systemd）

支持 Linux AMD64 与 ARM64。下面一条命令会从本仓库的最新 GitHub Release
自动下载匹配当前架构的二进制、校验 SHA-256，并安装为 systemd 服务：

要求服务器使用 systemd，并已安装 `curl` 或 `wget`；安装过程需要 `root` 或 `sudo` 权限。

```bash
curl -fsSL https://raw.githubusercontent.com/codermyxiaoc/sub2api-guardian/main/install.sh | sudo bash
```

管道安装无法交互输入，默认使用端口 `8787`。自定义端口：

```bash
curl -fsSL https://raw.githubusercontent.com/codermyxiaoc/sub2api-guardian/main/install.sh \
  | sudo bash -s -- --port 9090
```

默认仅监听 `127.0.0.1`。确实需要从局域网直接访问时可显式设置：

```bash
curl -fsSL https://raw.githubusercontent.com/codermyxiaoc/sub2api-guardian/main/install.sh \
  | sudo bash -s -- --port 9090 --listen 0.0.0.0
```

安装指定版本，不随最新 Release 变化：

```bash
curl -fsSL https://raw.githubusercontent.com/codermyxiaoc/sub2api-guardian/main/install.sh \
  | sudo bash -s -- --version v1.0.0 --port 8787
```

重复执行安装命令即可升级，SQLite 数据与页面配置不会被覆盖。无参数升级会保留已经写入
`/etc/sub2api-guardian.env` 的监听地址；传入 `--port` 或 `--listen` 时则使用新值。
离线安装仍可使用 `sudo bash install.sh --binary ./guardian-linux-amd64`；首次直接运行本地脚本且
未指定端口时，会交互提示端口。

脚本会创建低权限 `guardian` 系统用户并安装以下内容：

| 路径 | 用途 |
|---|---|
| `/opt/sub2api-guardian/guardian` | 程序文件 |
| `/var/lib/sub2api-guardian/` | SQLite 数据与运行状态，权限 `0700` |
| `/etc/sub2api-guardian.env` | 监听地址与数据目录 |
| `/etc/systemd/system/sub2api-guardian.service` | systemd 服务 |

常用运维命令：

```bash
systemctl status sub2api-guardian
journalctl -u sub2api-guardian -f
systemctl restart sub2api-guardian
```

安装完成后打开 `http://服务器地址:端口`。默认仅允许服务器本机访问；需要外部访问时，推荐
保持 `127.0.0.1` 监听并使用下文的 Nginx/Caddy 反向代理。

卸载程序但保留数据：

```bash
sudo systemctl disable --now sub2api-guardian
sudo rm -f /etc/systemd/system/sub2api-guardian.service
sudo rm -rf /opt/sub2api-guardian
sudo systemctl daemon-reload
```

数据保存在 `/var/lib/sub2api-guardian/`。确认不再需要配置、账号和历史数据后，可另行删除该目录，
以及 `/etc/sub2api-guardian.env` 和 `guardian` 系统用户。

### 下载运行

从 [Releases](https://github.com/codermyxiaoc/sub2api-guardian/releases) 下载对应平台的二进制，直接运行：

```bash
chmod +x guardian-linux-amd64
./guardian-linux-amd64
```

打开 `http://127.0.0.1:8787`，首次访问会进入**初始化向导**：

1. **创建管理员账号** —— 用户名 + 密码（至少 8 位）。密码以 PBKDF2-SHA256 摘要存储，无法反推
2. **连接 sub2api** —— 填地址与 Admin API Key（sub2api 后台「系统设置 → 管理端 API Key」）

提交时会真的连一次 sub2api 校验 Key，连不上就停在向导里告诉你原因，可以直接改了重试。
校验通过才会创建账号并自动同步分组与渠道。

![创建管理员账号](docs/images/setup-account.png)

![连接 sub2api](docs/images/setup-connection.png)

### 从源码构建

需要 Go 1.24+、Node 18+ 与 pnpm。

```bash
git clone https://github.com/codermyxiaoc/sub2api-guardian.git
cd sub2api-guardian
make build                 # 构建前端并编译成单个二进制
cd backend && ./guardian
```

> 前端由 `go:embed` 打进二进制，**必须先构建前端**再编译后端，否则只有占位页。
> `make build` 已经处理好顺序。

### Windows 一键测试包

在 Windows 上完成一个版本后，运行：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\build-windows-test.ps1
```

脚本会重新构建前端和 Windows AMD64 后端，并固定输出到主项目下的独立测试目录：

```text
../sub2api-guardian-test/
├── guardian.exe
├── start-guardian.cmd
├── build-info.txt
└── data/guardian.sqlite    # 首次运行后创建，重新打包不会删除
```

双击 `start-guardian.cmd` 即可启动并打开 `http://127.0.0.1:8787`。打包前如果旧的
`guardian.exe` 仍在运行，需要先关闭它，否则 Windows 会禁止覆盖正在运行的文件。

前端已经构建过、只需要重新编译 EXE 时，可以使用：

```powershell
.\build-windows-test.ps1 -SkipFrontend
```

---

## 部署

### 交叉编译

```bash
make build-linux
# 产出：
# dist/guardian-linux-amd64
# dist/guardian-linux-arm64
# dist/checksums.txt
```

无需 C 工具链：SQLite 用的是 `modernc.org/sqlite`（纯 Go 实现），`CGO_ENABLED=0`
即可交叉编译，产物是**静态链接的单文件**（`ldd` 报 "not a dynamic executable"），
不依赖 glibc 版本，也能直接塞进 `scratch` / `alpine` 镜像。

```bash
scp dist/guardian-linux-amd64 server:/opt/guardian/guardian
ssh server '/opt/guardian/guardian'   # 数据库自动建在 /opt/guardian/data/
```

#### 发布者：创建可供一键安装使用的 Release

`install.sh` 会按以下固定名称下载资产，名称不能改变：

- `guardian-linux-amd64`
- `guardian-linux-arm64`
- `checksums.txt`

执行 `make build-linux` 后，在 GitHub 创建版本标签和 Release，并上传上述三个文件。
使用 GitHub CLI 时可以执行：

```bash
gh release create v1.0.0 \
  dist/guardian-linux-amd64 \
  dist/guardian-linux-arm64 \
  dist/checksums.txt \
  --repo codermyxiaoc/sub2api-guardian \
  --title "v1.0.0" --generate-notes
```

仓库至少需要存在一个已发布的 Release，`/releases/latest/download/...` 才能工作；
仅把二进制提交到仓库目录里不能被远程安装脚本自动下载。

### Docker Compose

默认把 Guardian 映射到宿主机回环地址的 `8787` 端口，并把数据保存到项目下的
`backend/data/`：

```bash
docker compose up -d --build
docker compose logs -f guardian
```

自定义宿主机端口：

```bash
GUARDIAN_PORT=9090 docker compose up -d --build
```

Compose 默认以 UID/GID `1000:1000` 的非 root 用户运行，标准部署不需要额外填写 UID/GID。
若宿主机用户 UID/GID 不同，只需首次写入 `.env`，后续更新无需重复设置；同时确保
`backend/data/` 由该用户可写：

```bash
mkdir -p backend/data
chown -R "$(id -u):$(id -g)" backend/data
```

也可以复制 `.env.example` 为 `.env` 后修改。需要从其他机器直接访问时，显式设置
`GUARDIAN_BIND=0.0.0.0`；此时必须同时限制防火墙来源并配置 HTTPS 反向代理。

如果 sub2api 运行在 Docker 宿主机，初始化时填写
`http://host.docker.internal:8080`（端口按实际值修改），不要填写容器内的
`127.0.0.1`。如果两个服务在同一个 Docker 网络，直接使用 sub2api 的服务名。

备份与恢复数据目录：

```bash
tar czf guardian-data.tgz -C backend/data .
```

### 公网访问不到？先看监听地址

**默认只监听 `127.0.0.1`，仅本机可访问。** 典型症状是服务器上 `curl 127.0.0.1:8787`
能通、但公网打不开 —— 这不是防火墙或安全组的问题，而是进程根本没监听对外网卡。
启动日志会明确提示这一点。

```bash
ss -lntp | grep 8787
# 127.0.0.1:8787  → 只有本机能访问
# 0.0.0.0:8787    → 所有网卡都能访问
```

两种解法，**优先第一种**：

**① 反向代理（推荐）** —— 保持只监听回环，由 Nginx/Caddy 对外提供 HTTPS：

```nginx
server {
    listen 443 ssl;
    server_name guardian.example.com;

    location / {
        proxy_pass http://127.0.0.1:8787;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        # SSE 实时推送必需：不缓冲、不超时断流
        proxy_set_header Connection '';
        proxy_buffering off;
        proxy_read_timeout 1h;
        proxy_set_header X-Forwarded-Proto https;  # 让会话 Cookie 带上 Secure
    }
}
```

**② 直接对外监听** —— 简单，但面板会直接暴露在公网：

```bash
GUARDIAN_ADDR=0.0.0.0:8787 ./guardian
```

> ⚠️ Guardian 持有 sub2api 的 Admin API Key，能力等同 sub2api 管理员。
> 直接对外监听前**务必先完成初始化、设好登录密码**，并在安全组里只放行可信来源 IP。
> 没有 HTTPS 时会话 Cookie 不会带 `Secure`，明文网络里可能被截获。

### systemd

```ini
[Unit]
Description=Sub2API Guardian
After=network-online.target

[Service]
ExecStart=/opt/guardian/guardian
# 只监听回环，由反向代理对外提供 HTTPS（推荐）。
Environment=GUARDIAN_ADDR=127.0.0.1:8787
Restart=always

[Install]
WantedBy=multi-user.target
```

不必设置 `WorkingDirectory` —— 数据目录按可执行文件位置定位。

### 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `GUARDIAN_ADDR` | `127.0.0.1:8787` | 监听地址。**默认只监听回环，公网访问不到** |
| `GUARDIAN_PORT` | `8787` | 仅在未设置 `GUARDIAN_ADDR` 时生效 |
| `GUARDIAN_DATA_DIR` | **可执行文件同级的 `data/`** | SQLite 数据目录 |
| `SUB2API_BASE_URL` / `SUB2API_ADMIN_KEY` | — | 首次启动时预置连接配置 |
| `IMAGE2_URL_ENCRYPTION_KEY` | 内置默认值 | image2 上游 URL 的代理加密密钥；更换后旧代理 URL 失效 |

---

## 数据与备份

**没有配置文件。** 所有配置（调度倍率、分组排除、保底数量、阈值、连接信息、
登录账号、会话）都存在**一个 SQLite 文件**里，默认是可执行文件同级的
`data/guardian.sqlite`。启动日志会打印它的绝对路径：

```
数据目录: /opt/guardian/data
数据库:   /opt/guardian/data/guardian.sqlite
```

之所以按可执行文件定位而不是当前工作目录：后者会让「从哪个目录启动」决定用哪个库 ——
换个快捷方式、换个服务脚本的工作目录，就会开出一个全新的空库，
看起来就像配置每次重启都被重置。

备份只需拷走 `data/` 目录；迁移时把它和二进制一起搬走即可。

上游渠道管理的数据使用 `upstream_` 前缀表，与 Guardian 的分组、渠道缓存、样本和事件隔离。
余额快照、余额查询日志和告警事件保留 7 天；渠道配置、缓存、任务与邮件/企微设置不会随历史清理删除。

### 忘记密码

没有邮件找回。恢复方式是停掉服务后清空账号表，重启即会重新进入初始化向导
（其余配置不受影响）：

```bash
sqlite3 data/guardian.sqlite "DELETE FROM users; DELETE FROM sessions;"
```

---

## 工作机制

引擎每 15 秒心跳一次，但真正的网络调用各自受策略里的间隔约束：

```
心跳 → 同步分组与账号
     → 到期 API Key 渠道拉取上游倍率（失败保留最近成功快照）
     → 采样（真实流量优先，探针兜底）
     → 解析调度倍率（实时模式用上游快照；关闭实时后用人工设置或类型默认值）
     → 计算健康分（短期 × 占比 + 长期 × 剩余，凭据失效置零）
     → 决策：排除 → 回池 → 倍率阈值熔断 → 硬熔断 → 软熔断（保底 + 切换上限）→ 分级
     → 调权（权重预算 → load_factor / priority，带防抖冷却）
     → 扩缩容
     → 写回 sub2api（受自动执行开关控制，先抓基线、记动作日志）
     → 分组聚合与告警
```

### 数据来源

- `POST /admin/accounts/:id/test`（SSE）—— 测活与首字延迟
- `POST /admin/accounts/upstream-billing-probe/batch`、
  `POST /admin/accounts/:id/upstream-billing-probe` —— 所有 API Key 平台优先复用
  Sub2API 账户管理页面的原生探测；定时任务每批最多 20 个渠道，手动同步仍只请求单个渠道。
  调度采用 `effective_rate_multiplier`（用户覆盖后的基础倍率再乘当前峰值系数），旧响应没有该字段时
  兼容 `resolved_rate_multiplier`。只有旧版 Sub2API 对原生接口返回 404/405 时，才通过
  `GET /admin/accounts/data?ids=` 临时读取连接信息兼容查询。
  凭据不落库、不返回前端、不写日志；成功倍率独立保存，失败不会覆盖最近成功值。
  拉取周期在「策略配置 → 运营配置」中统一设置；渠道编辑中可单独开启实时倍率和超阈值熔断
- `GET /admin/ops/requests?account_id=` —— 真实流量的耗时、状态码、成功失败；
  **需要 sub2api 开启运维监控**，未开启时自动降级为纯探针模式并在页面顶部提示
- `PUT /admin/accounts/:id`、`POST /admin/accounts/:id/schedulable`、
  `clear-error`、`recover-state` —— 写回

### 多分组渠道怎么算

一个账号可能同时属于策略不同的多个分组，但 `priority` 与 `load_factor` 都是
**账号级**字段，无法为同一账号在不同分组里取不同值。因此权重取折中：
**在每个所属分组各按该组策略算一份权重，再取平均**，让各分组的诉求都被计入，
而不是让主分组单方面决定。

`priority` 的落地按分组重新编号，基准取组内原值的**中位数**——
这样重排后的区间与原区间重合，组内顺序按权重重排，**组间的相对位置保持不变**。

### 保底：每个分组至少留一个渠道

这是系统的第一原则，**硬熔断与「见到即熔断」同样受它约束**。一批渠道同时 401 时，
最后一个不会被熔断，而是转为「保底强留」并告警 —— 即使它大概率也不可用：
**分组断供由人来决策，不由自动化代劳**。

### 「待探测」不等于「不可用」

刚同步、还没被探测过的渠道计为 `pending`，且按 sub2api 侧的 `schedulable` 计入可用池。
没有证据说明它坏，就不假定它坏 —— 否则刚接入时分组健康矩阵会显示「可用 0」，
与网站上明明在正常服务的渠道对不上。

### 立即调度 vs 启动调度

| 按钮 | 作用 | 是否持久化 |
|---|---|---|
| **跑一轮** | 立刻执行一次：探测 → 判定 → 写回，跑完即止 | 不改任何开关 |
| **取消调度 / 启动调度** | 开关自动守护（心跳每 15 秒自动跑） | 存库，重启保持 |

取消调度会中断当前轮次并停止心跳，**重启后仍保持停止**；此时目录仍按 2 分钟
定时同步，页面数据不会变旧，只是不再探测、不再熔断、不再写回。

### 认证失效自动处置

渠道反复出现凭据失效时可以自动处置。**该功能默认关闭**，需显式打开。

| 动作 | 效果 | 可逆 |
|---|---|---|
| 暂停调度 | 加入暂停名单，不接流量但继续监控 | 是 |
| 停用账号 | 在 sub2api 里置为 `disabled`，保留凭据 | 是 |
| 删除账号 | 从 sub2api 删除 | **否** |

**删除不可逆**：sub2api 对 API Key 做了脱敏，Guardian 读不到凭据，
删除后无法重建，只能从你自己的凭据来源恢复。

五道守卫同时生效：

1. 最近 N 条样本里命中配置的错误码达到阈值
2. 状态需持续满最短观察时长，给人工介入留窗口
3. `keep_last_in_group` 为真时**绝不处置分组内最后一个渠道**
4. 每轮最多处置 `max_per_round` 个，防止配置失误清空池子
5. **限流渠道一律不处置**（限流会自愈，见上文）

删除前会先把 `schedulable` 摘掉，避免飞行中的请求打到即将消失的账号上。
任何一道守卫拦下时都会写 `cleanup_skipped` 事件说明原因，不会静默跳过。

---

## 登录与安全

- **全部接口需要登录**：除 `/healthz`、`/api/setup`、`/api/login` 之外，
  所有 `/api/*` 未登录一律返回 401
- **会话用 HttpOnly Cookie**：`SameSite=Strict`（挡 CSRF），JS 读不到（防 XSS），
  HTTPS 下自动加 `Secure`。有效期 14 天，使用中自动续期
- **Guardian 登录口令不存明文**：口令存 PBKDF2-SHA256（20 万次迭代 + 每人独立随机盐），
  会话只存令牌的 SHA-256 摘要 —— 数据库被读走也无法直接冒用会话
- **上游渠道凭据可逆保存并直接展示**：上游密码和 New API 系统令牌需要用于远端登录，
  已登录 Guardian 的用户可以查看和复制；请保护数据库文件并限制面板访问。Sub2API 的临时
  access / refresh token 不通过 API 返回，SMTP 密码和企微应用 Secret 也只显示“已配置”状态
- **改密码会吊销其他会话**，只保留当前这一个
- **CORS 不使用通配符**：生产同源，只有开发端口在白名单里

---

## 开发

```bash
make dev-backend    # 后端 127.0.0.1:8787
make dev-frontend   # 前端 127.0.0.1:5177（自动代理 /api）
make test           # go vet + go test + vue-tsc
make fmt            # gofmt -w
```

### 目录结构

```
backend/
  cmd/guardian/          启动入口
  internal/
    config/              环境变量与数据目录解析
    domain/              核心类型
    policy/              全部开关与阈值 + 分组覆盖合并
    auth/                口令哈希与会话令牌（标准库 pbkdf2）
    store/               SQLite：策略、缓存、样本、状态、基线、事件、账号
    upstream/            sub2api 管理端客户端
    channelmanager/      独立上游渠道同步、余额、令牌、告警、SMTP 与企微应用
    scoring/             错误分类与健康分（纯函数）
    engine/              采样、决策、调权、扩缩容、写回、分组聚合
    api/                 HTTP 接口与 SSE
    web/                 内嵌前端
frontend/                Vue 3 + TypeScript + Tailwind + Pinia + Vue Router
```

### 测试

覆盖的关键不变量：

- **健康分**：事件分映射、短期加权、长期均值、凭据失效否决、分位统计
- **保底**：硬熔断同样受约束、`aliveCount` 口径、survivor 不重复留
- **限流**：不熔断、不摘调度、不被清理、健康分不归零（即使显式配置 429）
- **写回一致性**：写回失败不落库为目标状态、下一轮继续重试
- **鉴权**：遍历路由表验证每条 `/api/*` 未登录返回 401
- **数据目录**：与工作目录无关、`go run` 临时目录回退
- **迁移**：老库补新字段、不覆盖用户显式设置、只执行一次
- **上游渠道**：独立表级联、Sub2API/New API 同步、401 映射、告警冷却、SMTP/企微应用通知与密钥掩码

```bash
cd backend && go test ./...
```

---

## 从旧版本升级

直接替换二进制即可，数据库会自动迁移：补齐新增字段、把历史上误判为「致命错误」的
限流样本重新归类。用户显式设置过的开关不会被迁移覆盖。

升级前建议备份 `data/` 目录。
