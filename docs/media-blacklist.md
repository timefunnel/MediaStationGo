# 我的拉黑

MSG 的「个人观影 → 我的拉黑」沿用收藏页的海报卡片样式。媒体详情页可以加入或解除拉黑，拉黑页可以直接解除；菜单和操作与收藏共用 `can_favorite` 权限。

- 数据按登录账户隔离，不删除媒体、文件、收藏或观看记录。
- 电影使用当前媒体库的现有 Emby 版本分组，隐藏同组的全部版本。
- 剧集使用现有 Emby 剧集标识，隐藏整剧的所有季、集，以及以后加入的同剧季集。
- MSG 保留媒体展示和管理入口。Emby 的列表、搜索、收藏列表、最近添加、续播、下一集、计数、详情与播放信息遵循拉黑过滤。
- 原媒体已移除或当前账户已无权查看时，拉黑页不返回其媒体详情，仍可解除该拉黑记录。

## API

以下路由均位于现有认证 API 前缀下，账户取自登录身份：

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/blacklist` | 当前账户的拉黑列表 |
| POST | `/media/:id/blacklist` | 幂等加入拉黑；单集自动按整剧处理 |
| DELETE | `/media/:id/blacklist` | 幂等解除对应影片或整剧的拉黑 |
| GET | `/media/:id/blacklist/status` | 查询当前影片或整剧的拉黑状态 |
| DELETE | `/blacklist/:id` | 根据拉黑记录 ID 解除，支持原媒体已移除 |

## 查询与缓存

`media_blacklists` 的复合唯一索引覆盖 `user_id, scope, target_id, library_id`。Emby 在统一可见性 SQL 中通过 `NOT EXISTS` 排除当前账户拉黑的剧集、电影版本组，之后才分组、计数和分页。Emby 列表请求不加载拉黑列表到 Go 中，也不逐个影片查询拉黑状态。

拉黑变更成功后增加该账户的缓存修订号，使原有 Items、Series、Latest 缓存失效；其他账户的缓存不受影响。数据库读写失败显式返回错误。

## 本地验证

- `go test ./internal/service ./internal/handler ./internal/model ./internal/database -count=1`
- `npm run build`（`web/`）及改动文件的 ESLint 检查。
- `TestMediaBlacklistErrorsAndIndexedSQL` 使用 SQLite `EXPLAIN QUERY PLAN` 检查两个排除子查询均使用复合索引。

本地测试覆盖账户隔离、整剧及新增季集、多版本电影、分页和总数、缓存更新、接口别名、认证权限、失败显式返回和原媒体已移除后的解除。未执行生产 PostgreSQL 执行计划或真实 Emby 客户端验证。
