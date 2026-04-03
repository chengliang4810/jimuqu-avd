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
      pfes-138-cover.jpg
      pfes-138-background.jpg
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

## 部署

```bash
docker compose build
docker compose up -d
docker compose logs -f avd
```

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
- `userAgent`: 抓取请求使用的 UA。
- `acceptLanguage`: 抓取请求语言头。
- `ffmpegPath`: `ffmpeg` 可执行文件名或路径。
- `proxy`: 全局代理地址，页面抓取、图片下载和 `ffmpeg` 视频分片下载都会使用它。容器内访问宿主机代理时可用 `http://host.docker.internal:7890`。
- `overwriteVideo`: 已存在时是否覆盖下载 `mp4`。
- `saveActorImages`: 是否尝试抓取演员头像。
- `categoryPageURL`: 自动扫描的分类页地址。
- `categoryScanIntervalSeconds`: 分类页扫描间隔，默认 `600` 秒。

## 说明

- `nfo` 里只写 `genre`，不再写 `tag`；同时会输出一张封面图和一张背景图。封面图由原始大图左半边裁切得到，背景图保留原图。
- 自动任务、状态文件和视频目录统一放在 `data/` 下，部署时只需要映射这一个目录。
- 常驻服务默认只依赖自动扫描；`-task` 参数仅用于临时手动补抓或调试。
- 常驻服务会尽量维持 `downloadConcurrency` 个并发下载槽位；有任务完成后，下一轮轮询会自动补上下一个任务。
- 容器镜像运行在 Debian bookworm，适合部署到 Debian NAS。
- 当前环境里没有本地 `ffmpeg` 可用于完整下载测试，因此仓库内完成了编译校验，但完整视频下载建议在 Docker 容器内验证。
