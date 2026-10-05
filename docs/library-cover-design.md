# 媒体库入口封面优化

核对日期：2026-10-06。参考项目：[g-steven037/emby-poster](https://github.com/g-steven037/emby-poster)，本次核对的 main 提交为 `982f9ef00684134c84af52a7b1317271e229b24b`。

## 调研结果

- [生成器源码](https://github.com/g-steven037/emby-poster/blob/982f9ef00684134c84af52a7b1317271e229b24b/generate_cover.py)通过 Emby API 取得最新电影与剧集素材，再用 Pillow 合成媒体库的横版主封面。它优化的是媒体库入口，不改变单部影视的海报。
- [配置](https://github.com/g-steven037/emby-poster/blob/982f9ef00684134c84af52a7b1317271e229b24b/config.py)提供两种 1920×1080 布局：六张海报横排加柔化背景，以及九张海报组成倾斜阶梯墙。中文名称与英文副标题需要额外字体和名称映射。
- 源码会静默忽略部分下载、解码、字体加载错误，素材不足时跳过生成；`UPDATE_INTERVAL_HOURS` 在生成器中没有调度实现。仓库在核对时未声明许可证。借鉴排版思路，独立实现，不复制其代码、字体、示例图片或容器。
- 生成器上传封面使用 `POST /emby/Items/{id}/Images/Primary`；当前 MediaStationGo 图片路由只有 GET/HEAD，不能直接接入它的上传步骤。现有按请求生成封面的链路可以复用，无需新增外部同步服务。

## 当前链路及改动

Emby 兼容端通过 `/Items/{library-id}/Images/Primary` 或 `/emby/Items/{library-id}/Images/Primary` 获取封面。`FolderCoverArtwork` 从现有数据库选取最多四张素材，沿用媒体库合并、剧集分组、图片去重与 ImageProxy 缓存。此次保留这条取图链路和素材数量，只替换图像合成方式。

此前，素材被裁切成铺满画布的等宽竖条；单图被横向展开，多图会裁掉海报侧边。本次按每张素材的原始比例等比缩放，居中排成轻微错落的画廊，加圆角和阴影，背景由第一张素材柔化并压暗。1～4 张素材都可正常呈现，不重复填充海报，不要求凑齐六张或九张。封面仍为 16:9，保持现有客户端的尺寸参数和 PNG 格式。

封面缓存版本从竖条样式升级到画廊样式，同时把素材 URL 纳入哈希；同一媒体更换海报 URL 时，媒体库封面的标识也会改变。现有的素材排序、单片图片标识和图片代理缓存策略保持原样。

Web 的入口缩略图原先独立使用四宫格。本次沿用现有预览数据与四张图片的请求参数，调整为柔化背景和竖版海报画廊；背景与首张海报使用同一个图片 URL。名称与条目数继续显示在卡片中，无需另建名称映射或字体资源。

## 验证与边界

- 本地 Go 定向测试覆盖：图片接口、16:9 尺寸、空库 404/no-store、1～4 张素材的边缘保留、横版素材比例、稳定输出，以及更换海报 URL 后的缓存标识变化。
- Web 执行 TypeScript/Vite 构建及改动文件的 ESLint 检查。
- 下图由实际新旧排版方式和离线绘制的示例海报生成，已作视觉核对；不是生产媒体库截图。
- 本次不调用 115/OpenList，不做真实云盘、电视设备或生产环境验证；不新增定时任务、上传接口，也不部署线上。

可复现的本地验证命令（均已通过）：

```powershell
go test ./internal/handler ./internal/service -run 'TestEmby(LibraryImage|LibraryViewExposesFolderCover|FolderCover|SQLFolderArtwork)' -count=1
git diff --check
# 在 web 目录中执行：
npm run build
npx eslint src/pages/LibrariesPageSections.tsx
```

Web 构建仍提示部分压缩后的 chunk 超过 500 kB；这是体积提示，不影响本次构建完成，本次不做无关的打包调整。

![旧竖条与新画廊排版对比](assets/library-cover-preview.png)
