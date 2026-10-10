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

## 按需获取（取消起播预热）

MSG 不再由 `PlaybackInfo` 请求（包括 `IsPlayback=true`）或视频 GET/HEAD 请求自动启动弹幕匹配、抓取。SenPlayer 和 Windows 均适用；只取消服务端起播预热，不取消播放器实际弹幕请求，Windows 客户端无需源码改动或重新打包。显式手动整季预热入口保留。

SenPlayer 请求 XML 弹幕、Windows 请求 JSON 弹幕时，才按需匹配和获取内容；已有媒体关联、手动匹配、本地导入和独立后端持久缓存仍复用原链路，不读取云盘文件，不改变源优先级。相同媒体、相同参数的并发弹幕请求共享在途任务，不同媒体或参数不混用结果；视频响应不等待弹幕任务。

按需取弹幕仍最多 3 个任务同时运行，超出并发时排队等待；任务记录最多 64 个，超过上限明确报不可用，不创建无界 goroutine。总超时复用 `danmaku.timeout_seconds`（默认 120 秒，含排队、匹配、取弹幕），服务关闭会取消并回收任务；单个播放器断开只取消自身等待。

成功的按需请求只共享在途任务，不另建 MSG 成功交接缓存，持久整集缓存仍只在独立后端。任务失败保留原错误、抑制同键重复请求 5 分钟，不伪装成空弹幕；原匹配失败/未命中的一天复用策略保持不变。手动换源、导入、偏移更新或清除关联成功后取消旧任务，避免返回旧源或旧时间轴。清除关联物理删除唯一行，允许随后重新匹配，不保留会冲突的软删除占位。

本地验证使用合成媒体、可阻塞的假后端和模拟云盘直链，不访问真实弹幕源、115、OpenList，不下载媒体。起播回归覆盖两端客户端标识与 UA、媒体来源选择、视频 GET/HEAD 和 Windows 原生视频路由，确认不匹配、不取弹幕；实际 XML/JSON 请求仍能获取内容并复用媒体关联。服务层原有失败冷却/到期、并发上限、客户端取消、服务关闭、参数隔离和关联更新测试继续保留。

验证记录（2026-10-09）：`go test ./internal/handler ./internal/service ./internal/config -count=1 -timeout=180s` 和 `go vet ./internal/handler ./internal/service ./internal/config` 通过；`go test ./internal/handler -run '^TestEmbyDanmaku(PlaybackDoesNotPrefetch|LoadsOnlyOnExplicitRequests)$' -count=20 -timeout=60s` 连续 20 轮通过。当前 Windows 环境 `CGO_ENABLED=0`，未执行 Go `-race`；本次未进行生产或两端实机验证。
