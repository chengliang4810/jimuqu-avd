# avd

`avd` 是一个面向 Debian/NAS Docker Compose 部署的后台抓取程序。任务来源于固定配置文件和任务文件，运行时只输出标准日志，不提供前端界面。

当前实现针对 Jable 视频页，支持把单个视频的全部数据写入统一目录：

```text
data/
  auto-tasks.txt
  state.json
  videos/
    pfes-138/
      pfes-138.mp4
      pfes-138.nfo
      poster.jpg
      fanart.jpg
      actors/
        actor-82a68478d0555cdea4ab75bfd5260209.jpg
```

## 特性

- 语言使用 Go，常驻内存占用低，适合后台轮询抓取。
- 视频下载交给 `ffmpeg`，直接处理 `m3u8` 到 `mp4` 封装。
- 配置集中在 `config/config.json`。
- 自动扫描任务集中在 `data/auto-tasks.txt`。
- 支持并行下载，默认同时下载 `5` 个视频，可通过配置调整。
- 状态持久化在 `data/state.json`。
- 自动抓取标题、发布日期、封面、背景图、演员并生成 `.nfo`。
- 默认每 10 分钟自动扫描一次中文字幕分类第一页 `https://jable.tv/categories/chinese-subtitle/`，发现新详情页后自动入队。

## 目录说明

- `config/config.json`: 主配置文件。
- `data/auto-tasks.txt`: 自动扫描追加的任务列表。
- `data/state.json`: 任务状态和失败次数。
- `data/videos/`: 最终输出目录，便于直接复制到媒体库。

## Docker 部署

### 方式 1：克隆源码后用 Docker Compose 构建

仓库根目录已经自带 `docker-compose.yml`，直接执行：

```bash
git clone https://github.com/chengliang4810/jimuqu-avd.git
cd jimuqu-avd

docker compose up -d --build
docker compose logs -f avd
```

如需代理，直接通过环境变量传入，不需要修改 `config/config.json`：

```bash
AVD_PROXY=http://192.168.1.10:7890 docker compose up -d --build
```

### 方式 2：不克隆仓库，直接使用 GHCR 镜像

镜像已经内置默认配置；如果你接受默认行为，只需要映射一个 `data` 目录即可。下面的代理地址 `http://192.168.1.10:7890` 只是示例，请改成你自己宿主机的实际代理地址。默认使用 `latest` 镜像，如需固定版本，把 `latest` 改成具体版本号即可。

```yaml
services:
  avd:
    image: ghcr.io/chengliang4810/jimuqu-avd:latest
    container_name: avd
    restart: unless-stopped
    environment:
      AVD_PROXY: http://192.168.1.10:7890
      MAX_RETAINED_VIDEOS: 100
    volumes:
      - ./data:/app/data
    logging:
      driver: json-file
      options:
        max-size: "10m"
        max-file: "3"
```

启动命令：

```bash
docker compose pull
docker compose up -d
docker compose logs -f avd
```

如果你要换地址，启动前传入环境变量即可：

```bash
AVD_PROXY=http://192.168.1.10:7890 docker compose up -d
```

如果你要限制最多保留 100 个已完成视频目录，可在 `environment` 里设置：

```yaml
MAX_RETAINED_VIDEOS: 100
```

如果你需要自定义其他配置，再额外挂载 `./config:/app/config`。

默认不再依赖手工维护任务文件。程序会每 10 分钟扫描一次中文字幕分类第一页，把新发现且未下载、未在下载中的视频自动写入 `data/auto-tasks.txt`，随后按队列顺序下载。

## 自动发布

仓库已配置 GitHub Actions 工作流 `.github/workflows/release.yml`：

- 每次 `push` 或合并到 GitHub `main` 分支时自动触发。
- 自动执行 `go test ./...`。
- 自动打包 Release 附件，并创建对应的 GitHub Release。
- 自动推送 GHCR 镜像：`ghcr.io/chengliang4810/jimuqu-avd:<version>` 和 `ghcr.io/chengliang4810/jimuqu-avd:latest`。
- 版本号默认按 `v1.0.<run_number>` 递增；如果要切换大版本或小版本，可修改工作流里的 `VERSION_SERIES`。

程序本身也会写入构建版本，可直接查看：

```bash
avd -version
```

## 常用命令

单次执行：

```bash
docker compose run --rm avd -config /app/config/config.json -once
```

临时只处理一个指定任务：

```bash
docker compose run --rm avd -config /app/config/config.json -task pfes-138
```

## 配置项

- `baseUrl`: 站点根地址。
- `pollIntervalSeconds`: 后台轮询间隔。
- `downloadConcurrency`: 同时并行下载的视频数量，默认 `5`。
- `httpTimeoutSeconds`: HTTP 超时。
- `maxRetries`: 单次请求或任务的最大重试次数。
- `autoTaskFile`: 自动扫描任务文件路径。
- `stateFile`: 状态文件路径。
- `videosRoot`: 视频输出根目录，默认固定为 `../data/videos`。
- `maxRetainedVideos`: 最多保留多少个已完成视频目录；`0` 表示不限制。Docker 部署可通过环境变量 `MAX_RETAINED_VIDEOS` 覆盖，容器每次启动都会读取当前环境变量值。比如设为 `80` 时，超过 80 部后会自动删除最旧的视频目录。
- `userAgent`: 抓取请求使用的 UA。
- `acceptLanguage`: 抓取请求语言头。
- `ffmpegPath`: `ffmpeg` 可执行文件名或路径。
- `proxy`: 全局代理地址，页面抓取、图片下载和 `ffmpeg` 视频分片下载都会使用它。Docker 部署建议通过环境变量 `AVD_PROXY` 传入，例如 `http://192.168.1.10:7890`；如果程序直接跑在宿主机上，才适合使用 `http://127.0.0.1:7890` 这一类本机回环地址。
- `overwriteVideo`: 已存在时是否覆盖下载 `mp4`。
- `saveActorImages`: 是否尝试抓取演员头像。
- `categoryPageURL`: 自动扫描的分类页地址。
- `categoryScanIntervalSeconds`: 分类页扫描间隔，默认 `600` 秒。

## 说明

- `nfo` 里只写 `genre`，不再写 `tag`；同时会输出 `poster.jpg` 和 `fanart.jpg`。封面图由原始大图左半边裁切得到，背景图会统一转成 `fanart.jpg`。
- 自动任务、状态文件和视频目录统一放在 `data/` 下，部署时只需要映射这一个目录。
- 下载中的视频会先写到 `data/tmp/<videoID>/<videoID>.mp4`，完成后再移动到 `data/videos/`。
- 常驻服务默认只依赖自动扫描；`-task` 参数仅用于临时手动补抓或调试。
- 常驻服务会尽量维持 `downloadConcurrency` 个并发下载槽位；有任务完成后，下一轮轮询会自动补上下一个任务。
- 如果设置了 `maxRetainedVideos`，程序会按完成时间从旧到新清理超出的成品视频，并同步从自动任务列表里移除，避免被下一轮自动重下。
- 容器镜像运行在 Debian bookworm，适合部署到 Debian NAS。
- 当前环境里没有本地 `ffmpeg` 可用于完整下载测试，因此仓库内完成了编译校验，但完整视频下载建议在 Docker 容器内验证。

## 飞牛 fnOS FPK 打包

仓库已内置飞牛 FPK 打包目录 `packaging/fpk`，形态是无前端 Docker 应用。FPK 安装后由飞牛应用中心通过 Docker Compose 拉取并运行 `ghcr.io/chengliang4810/jimuqu-avd:latest` 镜像。

Windows 本地打包：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\build-fpk.ps1
```

如果已经安装 `fnpack`，也可以直接执行：

```bash
fnpack build --directory packaging/fpk
```

通过脚本打包时，产物会生成在 `dist/fpk/jimuqu-avd.fpk`，可在飞牛应用中心手动安装。直接执行 `fnpack build --directory packaging/fpk` 时，`fnpack` 会把产物放到当前工作目录。安装后配置文件和运行数据位于“文件管理 - 应用文件 - jimuqu-avd”：

- `config/config.json`: 应用配置，可在这里配置代理、并发数、保留数量等。
- `data/`: 自动任务、状态文件和下载产物目录。

安装后也可以直接在飞牛“应用设置”里修改常用项，包括代理地址、最多保留视频数和同时下载数量。最多保留视频数填 `0` 表示不自动清理；其他参数使用包内默认配置。

FPK 不在安装时构建镜像，因此发布新版程序时需要先通过 GitHub Actions 推送新版 GHCR 镜像，再重新打包或安装 FPK。

### Native FPK

如果飞牛设备本机已经提供可用的 `ffmpeg`，也可以打原生包。原生包会把 Linux amd64 的 `avd` 二进制放进 FPK，不依赖 Docker 和 GHCR 镜像。

飞牛设备上建议先确认：

```bash
which ffmpeg
ffmpeg -hide_banner -protocols | grep -E 'https|tls|crypto|httpproxy'
```

Windows 本地打包 Native FPK：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\build-fpk-native.ps1
```

产物会生成在 `dist/fpk/jimuqu-avd-native.fpk`。安装后配置文件和运行数据位于“文件管理 - 应用文件 - jimuqu-avd-native”。
Native FPK 同样提供飞牛“应用设置”，保存后会写回 `config/config.json`；如果应用正在运行，设置保存后会自动重启后台进程使配置生效。
