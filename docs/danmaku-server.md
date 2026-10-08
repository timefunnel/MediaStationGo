# 独立弹幕后端

弹幕服务不再依赖 `resource_import.enabled`、pipeline 地址或凭据。MSG 保留公开弹幕端点、媒体关联、手动偏移与本地文件原文，服务端负责多源优先级、精确季集匹配、缓存和归一化。

```yaml
danmaku:
  enabled: true
  url: http://127.0.0.1:9322
  token: replace-with-a-random-token-at-least-16-characters
  timeout_seconds: 120
```

对应环境变量：`MEDIASTATION_DANMAKU_ENABLED`、`MEDIASTATION_DANMAKU_URL`、`MEDIASTATION_DANMAKU_TOKEN`、`MEDIASTATION_DANMAKU_TIMEOUT_SECONDS`。地址必须从 MSG 运行环境可达，token 与独立服务一致；启用但配置缺失时启动校验失败。

匹配信息直接复用 MSG 数据库已识别的标题、原名、TMDB ID、季、集与年份，不传媒体路径，不反向登录 MSG，不读取云盘。整季预热也发送每集已有识别字段。客户端（包括 SenPlayer）原 MSG 弹幕 API 地址不变。

源与优先级在独立服务的 `DANMAKU_PROVIDERS` 中设定。默认 `tencent,iqiyi,dandanplay`；高优先级唯一命中即停止，弹弹play 优先级最低。前两源未准确命中且本地缓存未命中时，才使用官方计次接口。旧未命中/失败关联按新策略迁移一次，新未命中关联一天内复用；新失败关联一天内抑制重试但继续返回错误；已有成功、手动或本地导入关联保留，不擅自换源。删除旧关联所使用的源后会明确报错，不能把该源的节目编号转交另一源。

独立服务源码、运行配置、固定第三方版本与许可说明见 media-pipeline 仓库的 `danmaku-server/README.md`。发布需要两个仓库的精确提交和两个独立镜像；仅验证或提交不等于正式发布。

## 起播并行准备（SenPlayer / Windows）

MSG 在已鉴权、已确认媒体来源的 `PlaybackInfo?IsPlayback=true` 请求，以及视频 GET 请求时，后台启动当前媒体的准确匹配和整集弹幕抓取。只识别现有客户端标识 `SenPlayer` / `MediaStation Windows` 及其对应 UA；详情查询、HEAD 探测和其他客户端不会触发。Windows 客户端已有起播标志和 `with_related=true` 弹幕请求，不需要重新打包。不会读取云盘文件、预取其他集或改变匹配优先级。

视频响应不等待弹幕任务。SenPlayer 的 XML 和 Windows 的 JSON 弹幕请求（`with_related=true, ch_convert=0`，无请求级偏移）共享同一个任务：已完成直接交接，未完成只等剩余时间。不同媒体或不同弹幕参数不混用结果；已有关联、手动匹配、本地导入和后端持久缓存仍复用原链路。

全局最多 3 个准备/取弹幕任务同时运行；后台提前准备不排队，忙时明确记录 skipped，播放器真正请求时仍可排队等待。任务及交接记录最多 64 个，超过上限明确报不可用，不创建无界 goroutine。准备总超时复用 `danmaku.timeout_seconds`（默认 120 秒，含排队、匹配、取弹幕），服务关闭会取消并回收任务；单个播放器断开只取消自身等待。

起播准备成功的结果仅保留 1 分钟用于交接，持久整集缓存仍只在独立后端；普通按需请求只共享在途任务，不另建长期成功缓存。任务失败保留原错误、抑制同键重复请求 5 分钟，不伪装成空弹幕；原匹配失败/未命中的一天复用策略保持不变。手动换源、导入、偏移更新或清除关联成功后取消旧任务并废弃交接结果，避免返回旧源或旧时间轴。清除关联物理删除唯一行，允许随后重新匹配，不保留会冲突的软删除占位。

本地验证只使用合成媒体、阻塞式假后端和模拟云盘直链，不访问真实弹幕源、115、OpenList，不下载媒体。覆盖起播不阻塞、两端及重复视频请求共享一次抓取、失败冷却/到期、并发上限、客户端取消、服务关闭、参数隔离和关联更新。

验证记录（2026-10-08）：`go test ./internal/service ./internal/handler ./internal/config -count=1 -timeout=180s` 通过，1974 项通过（含子测试）；`go test ./internal/service -run '^TestDanmakuPlayback' -count=20 -timeout=60s` 连续 20 轮通过；`go vet ./internal/service ./internal/handler ./internal/config` 通过。Go `-race` 因当前 Windows 环境 `CGO_ENABLED=0` 无法运行；未验证生产或两端实机首帧时序，不能保证弹幕一定在首帧前就绪。Windows 客户端无需源码改动或重新构建。
