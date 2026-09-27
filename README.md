# qb-stream

把 qBittorrent **正在下载**的视频以 HTTP 流的方式代理出来，配合顺序下载实现**边下边看**：第一集刚开始下载几分钟就能在 VLC、PotPlayer、IINA、SenPlayer 等播放器里直接看，看完一集下一集也已缓冲好。

专为家庭 NAS 局域网场景设计：单文件 Go 程序 + 内嵌网页（Vue 3，无额外运行时），Docker 一条命令部署，无需登录认证。

## 功能特性

- **边下边看**：只等待播放位置前的少量数据就绪（起始 2 个 piece），播到未下载区域时逐块等待，支持播放器拖动进度条（HTTP Range）
- **顺序下载追剧**：添加种子时自动开启「顺序下载 + 首尾 piece 优先」；后台每 30 秒自动把下一集提为高优先级，第一集播完下一集正好下好（严格逐集，不跳集）
- **网页管理**（电脑 / 平板 / 手机自适应）：
  - 添加种子（.torrent 文件或磁力链接）、开始 / 暂停 / 删除 / 删除并删文件
  - 种子详情：进度、下载 / 上传速度、剩余时间、分享率、做种 / 下载人数、添加与完成时间、Tracker
  - 每个种子一键复制**播放地址**和 **M3U 剧集列表地址**
- **M3U 选集连播**：播放器（VLC / SenPlayer 等）打开列表地址即可在播放器内选集，播完自动切下一集
- **路径全自动映射**：自动探测 qBittorrent 默认下载目录，不硬编码任何挂载路径，换环境零配置
- 配置网页化：qB 连接、监听端口、流参数等都能在网页「设置」里改，保存即时生效（端口需重启）

## 工作原理

1. qb-stream 调用 qBittorrent WebUI API 获取种子、文件列表与每个 piece 的下载状态
2. 播放器请求 `/stream` 时，服务端定位对应文件，等待起始窗口的 piece 下载完成后开始响应
3. 播放过程中每发送一块数据前确认该块已落盘，未下载则轮询等待（可配置超时），已下载区间支持任意 Range 拖动
4. qB 开启顺序下载，后台协程滚动提升下一集文件优先级，保证观看速度跟得下载速度

## 部署

### 前提

- 一台运行 [qBittorrent](https://www.qbittorrent.org/)（4.6+，含 WebUI）的 NAS 或服务器，下载任务正常
- 已安装 Docker（推荐）；或自行准备 Go 1.26+ 裸机运行

### 方式一：Docker Compose（推荐）

#### 1. 获取镜像（二选一）

**A. 自己构建镜像**（任意一台装了 Docker 的电脑，构建完拷到 NAS 即可，NAS 上不需要编译环境）：

```bash
git clone <你的仓库地址> qb-stream
cd qb-stream
docker build -t qb-stream:latest .
docker save qb-stream:latest -o qb-stream-image.tar
```

把 `qb-stream-image.tar` 传到 NAS，在 NAS 的 Docker 管理界面（如飞牛 fnOS、群晖 Container Manager、unRAID）选择「本地镜像导入」。

**B. NAS 上直接从源码构建**（NAS 能联网拉取 golang 镜像时）：

把整个项目目录放到 NAS 上，将 `docker-compose.yml` 里的 `image: qb-stream:latest` 改成 `build: .`。

#### 2. 修改配置

编辑 `docker-compose.yml`，至少改两处：

```yaml
- QBSTREAM_QB_PASS=改成你的qB密码     # qBittorrent 的 WebUI 登录密码
volumes:
  - /path/to/your/downloads:/media:ro  # 冒号左边改成 qB 下载目录在 NAS 宿主机上的真实路径
```

- `QBSTREAM_QB_URL`：qb-stream 与 qB 在同一台机器时用 `http://127.0.0.1:qB的WebUI端口`（compose 默认 host 网络）；qB 在别的机器上就填那台机器的地址
- 下载目录路径要和 qBittorrent 容器下载目录映射的**宿主机路径一致**，容器内统一挂载为 `/media`（只读），路径前缀由程序自动探测匹配，无需关心 qB 容器内是 `/qbs` 还是 `/downloads`

#### 3. 启动

```bash
docker compose up -d
docker compose logs -f qb-stream   # 看到「已登录 qBittorrent」即成功
```

浏览器打开 `http://NAS_IP:8888`（端口在 compose 的 `QBSTREAM_PORT` 改）。

> 说明：环境变量只在**首次启动**（配置文件不存在）时用于引导生成配置，之后配置持久化在 `./config/qb-stream.json`，所有修改请走网页「设置」页。

### 方式二：裸机运行

```bash
# 需要 Go 1.26+
go build -o qb-stream .

# 首次启动（参数仅首次用于生成 qb-stream.json，之后改配置文件或网页设置）
./qb-stream \
  --qb-url http://127.0.0.1:8080 \
  --qb-user admin \
  --qb-pass '你的qB密码' \
  --port 8888
```

程序启动后把 qB 的下载目录通过软链或直接运行在能访问该路径的机器上即可（非容器时默认按 qB 下载目录原路径访问；若路径不同，可在配置文件里设置 `path_map_from` / `path_map_to`）。

支持 `SIGINT` / `SIGTERM` 优雅关闭。

## 使用方法

1. **添加种子**：网页首页顶部粘贴磁力链接或选择 `.torrent` 文件，自动按顺序下载并立即开始
2. **看正在下载的视频**：
   - 单集：点种子卡片「文件」进入文件列表，对某一集点「复制播放地址」，粘贴到播放器「打开 URL / 打开网络串流」
   - 整部剧：种子卡片上直接点「复制列表」（或文件页顶部按钮）复制 M3U 列表地址，用播放器打开，即可在播放器内选集并自动连播
3. 播放建议等种子有少量进度（第一集开头几个 piece 已下载）再打开；已下载完成的任意位置可直接拖动
4. 种子管理：开始 / 暂停、删除（保留文件）、删除并连文件一起删除，均在网页上操作（危险操作有二次确认）

支持的视频格式：mp4、mkv、avi、mov、webm、ts、m4v、flv、wmv。

实测可用播放器：VLC（电脑 / 手机）、PotPlayer、IINA、SenPlayer（手机）。

## 配置项

| 配置文件键 / 环境变量 | 说明 | 默认值 |
| --- | --- | --- |
| `qb_url` / `QBSTREAM_QB_URL` | qBittorrent WebUI 地址 | `http://127.0.0.1:8080` |
| `qb_user` / `QBSTREAM_QB_USER` | qB 登录用户名 | `admin` |
| `qb_pass` / `QBSTREAM_QB_PASS` | qB 登录密码（必填，仅首次引导用） | 空 |
| `host` / `QBSTREAM_HOST` | 监听地址，局域网访问用 `0.0.0.0` | `127.0.0.1`（Docker 镜像内为 `0.0.0.0`） |
| `port` / `QBSTREAM_PORT` | 监听端口 | `8888` |
| `poll` | 播放中检查 piece 状态的轮询间隔（不低于 500ms） | `1s` |
| `max_wait` | 播放到未下载区域时的最长等待时间 | `120s` |
| `chunk` | 单次发给播放器的数据块大小（字节） | `65536` |
| `prio_lead` | 自动优先级领先集数 | `1` |
| `prio_interval` | 自动优先级检查间隔 | `30s` |
| `path_map_to` | 容器内下载目录挂载点（Docker 一般不用改） | `/media` |
| `path_map_from` | qB 路径前缀，留空 = 自动探测（推荐） | 空 |

配置文件为工作目录下的 `qb-stream.json`（权限 0600，内含 qB 密码，注意保密，**不要提交到 git**）。优先级：配置文件 > 环境变量 > 命令行参数。

## 常见问题

**Q：打开播放地址一直转圈 / 超时？**
暂停状态的种子不会继续下载，未下载的部分会等到超时。请在网页上点「开始」恢复任务；另外跳着点后面的集数也会等待较久，建议按顺序观看。

**Q：Docker 用 bridge 网络连不上宿主机的 qB（超时）？**
部分 NAS 的防火墙会拦截容器经网桥访问宿主机 IP。compose 已默认使用 `network_mode: host`，此时用 `http://127.0.0.1:qB端口` 访问 qB 最稳。

**Q：网页里能管理 qB，安全吗？**
本程序面向可信局域网设计，不提供登录认证，也不建议暴露到公网；如需公网使用，请自行放在 VPN / 内网穿透带鉴权的环境之后。

**Q：下载目录为什么不用手动配置映射？**
程序会调用 qB 的偏好设置接口自动获取默认下载目录，并依次尝试多种候选路径（含 `. !qB` 未完成文件后缀），适配不同人的挂载习惯。自动探测不符合预期时，才需要在配置文件手动设置 `path_map_from`。

## 技术栈

- 后端：Go 标准库 `net/http`，零第三方依赖；qBittorrent WebUI API 4.6+
- 前端：Vue 3（global build，通过 `go:embed` 内嵌进二进制），无 Node 构建步骤
- 镜像：多阶段构建（golang:1.26-alpine → alpine:3.20），镜像约 7MB

## 目录结构

```
qb-stream/
├── main.go              # 入口、路由、优雅关闭
├── config.go            # 配置文件 / 环境变量 / 命令行参数
├── qb_client.go         # qBittorrent WebUI API 客户端
├── stream_handler.go    # 流播放、Range、路径映射、M3U 列表
├── waiter.go            # piece 就绪等待
├── autoprio.go          # 自动滚动优先级
├── api.go               # 网页 JSON API
├── settings.go          # 网页设置接口
├── static.go            # 内嵌前端资源
├── web_ui.go            # 中文展示文案
├── static/              # Vue 3 前端（index.html / app.js / style.css）
├── Dockerfile
└── docker-compose.yml
```

## 许可证

MIT
