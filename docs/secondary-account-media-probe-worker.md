# 第二账号整库补齐：自动转存与独立 worker

## 状态

2026-10-27：本地代码已接入自动转存。管理员导出整库清单，独立节点按清单把文件转存到 B，核对来源和副本的 SHA1、大小，再复用信息或 probe，最后预览回填。技术字段仍只写入 MSG 的 media。当前未部署、未执行真实账号任务。

用户只需一次配置 A 的来源分享、B 授权和 B 专用暂存父目录，不需要逐批手动搬文件。A 分享由用户提供，worker 不持有 A 凭据，不替 A 创建分享；B 副本默认保留，不自动删除。容量不足、分享失效或转存受限时明确停止。

代码开发不授权真实执行。实际运行前仍须按项目规则报告目标、接口类别、预计数量、频率、并发、时长、真实请求及风险，取得覆盖相应范围的许可。以下 run 仅描述后续执行入口。

## 1. 导出及一次配置

管理员打开「系统任务 → 文件信息队列 → 第二账号离线补齐」，导出任务清单。沿用已保存的媒体库选择，空选择覆盖全部云端媒体。只读媒体记录、保存不可变任务快照，不访问云盘。清单含媒体 ID、库根、相对路径、大小、来源指纹和技术信息，不含 A 凭据或直链。

所有任务文件放在 Git 工作区以外的私密目录。以下命令都不联网：

```text
media-probe-worker credentials-template -out /private/secrets/115-probe-b.json
media-probe-worker sources-template -manifest /private/manifest.json -out /private/sources.json
media-probe-worker init -manifest /private/manifest.json -out /private/config.json
```

凭据文件填写 B 的 user_id、access_token、refresh_token、可选 expires_at（Unix 秒，未知填 0），以及 cookie。自动转存使用 B Cookie；列 B 目录、建目录、取链使用 B OpenAPI Token，二者必须属于同一用户。运行前分别请求实际身份核验，不仅检查文件中的用户 ID。已有副本模式不需要 Cookie。

用户按 [OpenList 开放平台授权说明](https://doc.oplist.org/guide/drivers/115_open)完成官方授权，通过自己的登录会话取得 B Cookie。CLI 不轮询二维码，不接收密码或验证码。不要将 Cookie/Token 放进聊天、Git、命令参数或日志，只提供私密文件路径。Linux 凭据及来源文件权限须 0600 或更严格，目录建议 0700；Windows 由用户限制私密目录 ACL。一个实例独占同一凭据文件，不与其他实例并行刷新同一令牌。

在 sources.json 中按库根填写分享来源，以下标识均为占位值：

```json
{
  "shares": {
    "root-id": {
      "root_id": "root-id",
      "relative_prefix": "",
      "share_url": "https://115.com/s/SHARECODE?password=CODE",
      "share_directory_id": "0"
    }
  }
}
```

share_directory_id 是分享中对应库根内容的目录 ID：只有分享顶层直接包含库内文件时才填 0；若顶层包含一个库文件夹，应填该文件夹 ID。链接含 #/list/share/ID 时，两处目录必须一致，也可将字段留空让链接指定目录。relative_prefix 表示分享目录对应的原库子目录，必须与批次配置一致。只接受 HTTPS 的 115.com、115cdn.com、anxia.com 分享，支持 password、pwd 或纯提取码 fragment。来源文件与账号凭据分开，离线规划不读取账号令牌。

编辑 config.json 一次：

- b_account_id：B 用户 ID。
- provenance：原样来源对应关系的说明，不填分享链接或口令；核实后将 confirmed_original_copy 设为 true，含义是确认来源一致，不要求事先手动转存。
- roots：模板包括所有存在缺口的库根。填写 b_directory_id，作为 B 专用暂存父目录，禁止账号根目录 0；source_id 对应来源文件别名，默认与 root_id 相同。
- 只处理子目录时，同步设置来源和根绑定的 relative_prefix，禁止重叠匹配。
- limits：本轮固定预算，不会因目录过大自动提高。refresh_before_run 默认关闭，鉴权失败不自动刷新重放。

init 中的媒体 ID 是最多 10 项、每库最多 2 项的样本建议。整库通过下面的 batches 自动选择所有基础字段或总码率有缺口的记录，不用手工挑后续媒体 ID。已有完整信息跳过。

## 2. 离线整库分批和计划

```text
media-probe-worker batches -manifest /private/manifest.json -batch /private/config.json -sources /private/sources.json -batch-size 10 -out /private/batches.json
media-probe-worker plan -manifest /private/manifest.json -batch /private/batches.json -batch-index 0 -sources /private/sources.json -out /private/plan-0.json
```

batch-index 从 0 开始；每批有独立 ID，只含该批所需库根和媒体。batches 生成有限清单，每项最多出现一次，不启动任务或授予访问许可。也可用 plan -batch /private/config.json 只规划样本。来源变更后重新生成批次和计划，旧计划不能访问新来源。

plan 输出完整目标、预算、风险和精确 digest，不输出分享链接、提取码或 Cookie。来源以摘要固定到计划，有效期最多 30 分钟。每轮应在已批准范围内使用精确摘要，不因账号登录就连跑整个集合。

自动转存模板预算：

| 项目 | 上限或约束 |
| --- | --- |
| 模板媒体数 | 10；配置上限 100 |
| 全部 115 控制请求 | 80 次，包含各分类及可选刷新 |
| 身份核验 | Token 1 次、Cookie 1 次 |
| 来源分享清单 | 20 次 |
| B 列目录 | 50 次，含暂存前后及恢复核对 |
| B 建目录、转存、取链 | 各 10 次 |
| 控制请求 | 串行，开始时间间隔至少 5 秒 |
| 媒体读取 | 60 次，每文件最多 6 次，开始间隔至少 1 秒 |
| 媒体响应体 | 全轮 320 MiB，每文件 32 MiB |
| probe | 单次最多 30 秒，两次之间至少 10 秒 |
| 全轮时长 | 15 分钟 |
| 共享查询 | 1 次，最多 100 个 SHA1 |
| OpenList refresh、创建 A 分享、删除 | 均不执行 |

分类上限不能相加突破总量。这些不是 115 官方安全阈值，仍可能限流、封禁、账号关联、容量不足或链路失败。增大批次不自动提高预算，达到上限即暂停。

## 3. 获准后的自动执行

```text
media-probe-worker run -manifest /private/manifest.json -plan /private/plan-0.json -sources /private/sources.json -credentials /private/secrets/115-probe-b.json -state /private/state-0.json -out /private/results-0.json -approve PLAN_DIGEST -ffprobe /usr/bin/ffprobe
```

程序先核验 Token 与 Cookie 均属于 B，只沿选中文件需要的分享路径列目录。缺文件、重名、尺寸不符或缺少 SHA1 时停止。每个来源文件使用 B 专用父目录下一个确定名称的私有暂存目录，名称由批次与来源身份摘要生成，避免不同路径的同名文件混在一起；不接管没有本任务写入意图的已有目录。

建目录及转存前持久化写入意图，只转存选中文件，不整目录搬运额外媒体。转存后读取 B 目录，要求唯一普通文件的名称、字节数、SHA1 与来源一致，再保存 B 文件 ID/PickCode。来源 ID 与 B ID 分开记录，结果包携带无秘密的来源摘要和转存证明供导入审计。

网络超时、响应失败、转存尚不可见或检查点保存失败均停止，不自动轮询、刷新或重试写入。新计划复用同一批检查点时，结果不明的建目录/转存只读取核对，空目录仍暂停，不重复提交；已确认副本也重新核对 B 文件身份，不再转存。重新提交失败写入需单独核对并安排新批次，不能删除检查点伪装首次执行。

映射后按 SHA1 与大小去重，先复用已有字段，再查共享库，只对仍有缺口的内容单次 probe。时长和大小已知可推算总码率并标记估算，不编造独立音视频码率。共享异常、缺项、大小冲突或解压失败均停止，不静默扩大成整批 probe。跨批可指定 -reuse-state /private/previous-state.json，仅复用同账号、同清单的内容缓存，不复用另一批的转存目录或文件 ID。

控制及媒体访问直接走节点 IPv4 出口，不继承生产代理或环境代理。ffprobe 只收到随机本机读取入口，直链不进子进程参数、结果包和普通日志。入口计数 Range、重定向和响应体，拒绝忽略有限 Range 的大响应。字节预算不含 HTTP/TLS/TCP 和底层预读。协议/容器白名单排除播放清单外链，首版没有进程级网络沙箱；ISO/BDMV 等明确记录为不支持。

取消中断当前读取。正常退出释放凭据锁，异常遗留 .lock 须核实无运行实例再处理。.started 记录计划使用，继续需新计划，不靠删标记重放。输出拒绝覆盖，每轮结果、检查点和计划使用独立路径。

已有 B 副本可显式使用旧模式：删除根绑定的 source_id，b_directory_id 指向已有原样副本根，使用原有限预算，不需 Cookie/来源文件。自动转存失败不会回退到此模式。

## 4. 预览与回填

在管理员页面预览实际文件、字段前后值、估算和未知项、冲突及失败，确认 A 文件导出后未替换、改名再导入。事务和行锁下重查身份与预览，防止播放补齐等并发变化造成覆盖，只补缺口；主队列运行或同片探测占用时拒绝导入。

回填同步相同来源队列状态、保留探测次数，不启动生产 probe。重复结果包不重复修改，审计保存原包和字段前后值；media_probe_exchanges 不是播放器数据源。媒体路径、标题、媒体库、file_hash 均不变。部分字段保留未知，“基础已齐”不代表可选字段、多音轨或字幕目录全部齐全。

接口沿用 POST /api/admin/media-probe-queue/exchange/manifest、/preview、/apply。文件/请求上限 64 MiB，每包最多 100 项。来源稳定性仍依赖用户的库根分享对应关系，MSG 快照不能实时发现 A 同路径同大小的替换。

## 验证与发布边界

复用现有 ProbeResult、ffprobe 解析器、媒体持久化和缓存失效。分享协议对照项目 media-pipeline/app/pipeline/client115.py 中 Share115Client，Cookie 身份核验对照 [p115client 源码](https://github.com/ChenyangGao/p115client/blob/main/p115client/client.py) 的 upload_info。未连接生产 pipeline，也不复制其账号配置。

测试采用合成记录、本机 HTTP 和模拟 probe，覆盖转存校验、预算、写入意图、不明结果恢复、去重、共享异常、取消、整库分批、预览失效与导入审计。真实 B 授权、分享限制、当前返回格式和实际 ffprobe 链路尚待获准后验证。

普通 MSG 镜像含 /usr/local/bin/media-probe-worker，不会自动启动；独立节点未来需覆盖 entrypoint 并关闭服务器健康检查。仍按精确 SHA 和不可变 digest 构建验证，当前不推送、不部署。
