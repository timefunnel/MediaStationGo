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

源与优先级在独立服务的 `DANMAKU_PROVIDERS` 中设定。默认 `tencent,iqiyi`；高优先级唯一命中即停止。只有配置 `dandanplay` 才使用官方计次接口。旧未命中/失败关联按新策略迁移一次，新未命中关联一天内复用；新失败关联一天内抑制重试但继续返回错误；已有成功、手动或本地导入关联保留，不擅自换源。删除旧关联所使用的源后会明确报错，不能把该源的节目编号转交另一源。

独立服务源码、运行配置、固定第三方版本与许可说明见 media-pipeline 仓库的 `danmaku-server/README.md`。发布需要两个仓库的精确提交和两个独立镜像；仅验证或提交不等于正式发布。
