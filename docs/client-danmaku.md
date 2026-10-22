# 播放器本地弹幕与账号同步

弹幕搜索、下载、缓存和清洗由播放器执行。MSG 只返回已识别的作品信息，并保存当前账号对当前物理媒体版本的源选择、节目页面 URL 和时间偏移；接口不会访问弹幕源或代理弹幕内容。

## 账号接口

使用现有 Emby 认证，在根路径和 `/emby` 前缀下提供：

`System/Info` 的 `ProtocolExtensions` 声明 `danmaku-associations` v1。播放器只在用户开启跨设备同步且服务端声明此能力时调用下述接口；未声明时使用标准媒体详情做本地匹配，选源和偏移只在本次播放生效，不探测新接口、不请求旧集中弹幕服务。同步开关关闭不影响本地搜索、下载和绘制。

- `GET /Items/:id/Danmaku?MediaSourceId=物理版本ID`
- `PUT /Items/:id/Danmaku?MediaSourceId=物理版本ID`

读取返回 `media_id`、`target`、`association`（没有关联时为 null）、字符串 `revision`。`target` 含 title、original_title、tmdb_id、season、episode、year，不含媒体路径、播放直链或源站凭据。

写入示例（占位地址不是实际节目）：

```json
{
  "source": "iqiyi",
  "episode_url": "https://www.iqiyi.com/v_example.html",
  "anime_title": "节目名",
  "episode_title": "本集标题",
  "offset_seconds": -2.5,
  "expected_revision": "0"
}
```

支持 tencent、iqiyi、youku 的规范 HTTPS 节目页面地址，不接受任意主机、查询参数、片段或显式端口。时间偏移范围为 ±600 秒，正值延后，负值提前。写入返回更新后的上下文；版本冲突返回 409，客户端应显示错误并重新读取，不能覆盖另一台设备的修改。

身份只来自认证上下文，禁止指定其他账号；版本必须属于请求条目且对用户可见。同账号另一台设备下次读取或播放时复用关联，不向正在播放的设备实时推送。不同物理版本的剪辑、片头可能不同，因此不共用偏移。

## 存储与退役边界

复用 `user_media_playback_preferences` 表的 user_id + media_id 唯一键，只新增弹幕字段和独立修订号；音轨、字幕偏好更新不会覆盖弹幕字段。启动时的既有 AutoMigrate 会增加这些字段，无需新表。

旧 `media_danmaku` 记录不自动转为账号记录：它们是全局数字节目引用，缺少账号归属，也可能依赖旧服务端 URL 注册表。新客户端直接保存源站页面 URL，不再生成数字节目编号或查映射。

旧 Web 集中抓取、手动导入和整季预热接口统一返回 410；旧 `/api/danmu/:id/raw`、`/api/danmaku/:id` Emby 弹幕路由移除。旧 `danmaku.enabled` 配置即便保留，也只会产生明确的退役日志，不初始化集中抓取客户端。旧服务代码与历史记录保留用于审计，不再挂载到这些路由。

本轮配套实现为 Windows 播放器。旧 Web 弹幕功能退役，TV 尚未接入本地模块；第三方播放器弹幕适配不再保留。实际部署的源码版本与不可变镜像以生产发布记录为准。

## 验证

`go test ./internal/service ./internal/handler -count=1 -timeout=180s` 验证账号与物理版本隔离、认证和伪造账号拒绝、冲突控制、URL 校验、音轨字幕互不覆盖、读取与保存均不产生源站流量，以及旧路由退役。
