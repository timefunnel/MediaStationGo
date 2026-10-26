# 第二账号离线补齐：本地实现与使用

## 状态与边界

实现对应[方案基线](secondary-account-media-probe-design.md)。代码包含管理员导出/导入、独立 CLI worker、请求预算、媒体读取入口、私密检查点和内容缓存。默认不运行任何远程任务，不自动登录、分享、转存或删除文件，不连接生产数据库。

worker 每轮最多 100 项，固定串行；整库通过多轮有限批次覆盖。默认批次模板最多 10 项、每个媒体库最多 2 项，优先选择基础技术信息或总码率缺失的记录。模板是建议范围，需要用户确认 B 的原样转存来源和目录绑定后才能生成有效计划。

**本说明中的 `run` 是后续经明确批准的执行入口；本地开发测试不运行真实 115/CDN 请求。** 开发授权不等于执行授权。开始实际任务前仍须报告真实目标、接口类别、数量、频率、并发、时长、refresh/真实请求情况以及限流、封禁、账号和链路风险。

## 1. 导出 MSG 清单

管理员打开「系统任务 → 文件信息队列 → 第二账号离线补齐」，选择「导出任务清单」。沿用已保存的媒体库选择，空选择覆盖全部云端媒体。导出读取媒体记录，并把不可变清单保存到 `media_probe_exchanges`，不列云盘目录、不取链。

清单包含媒体 ID、库根 ID、相对路径、文件大小、来源指纹和技术字段快照，不包含 A 凭据或直链。文件属于私密任务资料，应放在 Git 工作区以外，不提交到仓库。来源批次在转存至回填期间保持不改名、不替换。

## 2. 准备 B 的转存和凭据

由用户在官方客户端完成 A 分享、B 转存，保留目录层级并使用专用批次目录。核对容量、转存限制和来源对应关系。不能把 A 的 PickCode、文件 ID 或直链填成 B 的标识。

按 [OpenList 的开放平台授权说明](https://doc.oplist.org/guide/drivers/115_open)由用户完成官方登录和授权。CLI 不提供二维码轮询，也不代输密码或验证码。可以离线生成空凭据模板：

```text
media-probe-worker credentials-template -out /private/secrets/115-probe-b.json
```

在私密文件中填写：

| 字段 | 含义 |
| --- | --- |
| `user_id` | B 的用户 ID，用于核对实际授权身份 |
| `access_token` | B 的访问令牌 |
| `refresh_token` | B 的刷新令牌，仅明确安排刷新时使用 |
| `expires_at` | 可选，访问令牌到期的 Unix 秒时间戳，未知填 0 |

不要把令牌放进聊天、命令参数、环境变量或 Git。Linux 凭据文件必须 `0600` 或更严格，目录建议 `0700`；Windows 文件应由用户在私密目录内限制 ACL。用户只需提供私密文件路径。运行时通过同目录独占锁确保本 worker 不同时刷新同一份凭据；也不能把该令牌交给其他 OpenList 实例并行刷新。

## 3. 离线生成批次和计划

以下两步不联网，不读取令牌：

```text
media-probe-worker init -manifest /private/manifest.json -out /private/batch.json
media-probe-worker plan -manifest /private/manifest.json -batch /private/batch.json -out /private/plan.json
```

先编辑 `batch.json`：

- `b_account_id`：B 的用户 ID，必须与凭据文件及后续实际身份一致。
- `media_ids`：本轮固定的媒体 ID，最多 100 个，必须来自原清单。
- `roots`：每项包含清单的 `root_id`、B 专用目录的 `b_directory_id` 和可选 `relative_prefix`。禁止绑定账号根目录 `0`。
- `relative_prefix`：如果 B 目录只转存了原库的一个子目录，填写该子目录相对库根的路径。例如原文件为 `Show/Season 1/A.mkv`，B 的指定目录直接包含 `A.mkv`，则前缀填 `Show/Season 1`。空值表示 B 指定目录对应整个库根；不允许重叠匹配。
- `confirmed_original_copy`：用户核实原样转存后设为 `true`；`provenance` 填批次来源说明，不填分享链接或口令。
- `limits`：有限执行预算，默认值见方案。必须按实际选片重新审阅，代码不会因目录过大自动提高上限。
- `refresh_before_run`：默认 `false`。只有明确批准本轮开始前刷新一次令牌才设 `true`；刷新计入控制请求总量。鉴权失败绝不自动刷新重放。

`plan` 输出完整目标、风险说明、批次和预算，并生成 `digest`。计划最长有效 30 分钟、只能执行一次；过期、修改范围、进程中断后继续均需要重新生成计划和批准相应范围。空模板不会静默选择账号、目录或转存来源。

## 4. 后续批准后的有限执行

```text
media-probe-worker run -manifest /private/manifest.json -plan /private/plan.json -credentials /private/secrets/115-probe-b.json -state /private/state.json -out /private/results.json -approve PLAN_DIGEST -ffprobe /usr/bin/ffprobe
```

`PLAN_DIGEST` 必须替换为经过审阅的精确摘要，不会从计划文件自动表示同意。输出文件拒绝覆盖；检查点允许原子更新。执行不通过生产 OpenList，不使用生产代理，控制及媒体客户端固定使用节点的公网 IPv4 出口；只有 B 的目录列表、身份核验、取链以及计划中显式开启的一次刷新可接触 115 控制接口。

worker 只沿指定文件需要的目录层级分页，不递归扫描其他目录。目录页仅持久化本批所需条目，避免大目录不断放大检查点。匹配依靠原样转存的批次来源、相对路径和精确大小；随后保存 B 返回的 SHA1，按 SHA1 和大小去重。没有可靠对应关系时不按片名猜测。

先复用已确认同内容的信息，再查询共享库，只对有效未命中及仍缺基础信息的内容 probe。已有时长和大小可推算总码率并标记估算，不为总码率单独读取媒体；之后若得到真实码率，节点缓存优先采用真实值。编码、分辨率等实质性字段冲突会停止，既有容器别名及码率等可选值不被静默覆盖。

共享服务超时、缺项、返回错误、哈希/大小冲突或解压失败都会停止，不把服务失败当作未命中。共享响应和解压大小受限，只查询、不上传媒体信息。跨批可显式指定 `-reuse-state /private/previous-state.json`，只复用同账号、同清单的内容信息，不复用另一批的文件 ID 或目录页。

ffprobe 仅接收随机的本机读取入口，签名直链不进入子进程参数、结果包和普通日志。读取入口计数上游 Range、重定向和响应体字节；忽略受限 Range 的大文件响应会被拒绝。协议及容器白名单排除播放清单等派生外链输入，首版不提供进程级网络沙箱。32 MiB 等字节限制指应用读取的响应体，不含 HTTP/TLS/TCP 和底层预读开销。

任一失败即停止后续工作，没有自动重试或 ffmpeg 二次读取。取消会中断正在读取的响应。正常退出会释放令牌锁；异常退出留下的 `.lock` 必须先确认没有运行实例再人工处理。一次性 `.started` 标记保留执行历史，不能删除它来重放旧计划。检查点不是自动恢复触发器，继续需要新计划。

## 5. 预览并导入结果

将 `results.json` 带回 MSG，在同一页面选择「导入结果并预览」。服务端从数据库加载原始清单，核对来源指纹、媒体 ID、大小和结果包摘要；预览列出实际文件、将修改的字段、估算字段、仍未知字段、冲突及失败。

用户确认原文件未替换、未改名并核对修改后，才点击确认导入。服务端在事务和行锁下重新预览，防止预览后播放补齐或文件变化造成覆盖；只补缺口，保留现有有效数据。主库批量队列运行或同片探测占用时拒绝导入。导入同步更新相同来源的队列完成/部分未知状态，不增加探测次数，也不启动队列。重复导入同一结果包不重复写入。

技术信息仍写入 `media`，复用现有 `ProbeResult`、ffprobe JSON 解析器、持久化函数和媒体缓存失效。`media_probe_exchanges` 中的导入记录保存原结果包以及字段前后值用于审计，不作为播放信息来源。文件 ID、路径、媒体库、标题及 `file_hash` 均不修改。部分基础字段仍缺失时不把探测版本提升为成功；“基础信息已补齐”不表示所有可选技术字段都有值。

管理员接口：

| 接口 | 行为 |
| --- | --- |
| `POST /api/admin/media-probe-queue/exchange/manifest` | `{library_ids: []}` 导出并保存清单 |
| `POST /api/admin/media-probe-queue/exchange/preview` | 直接提交结果包，返回字段预览与摘要 |
| `POST /api/admin/media-probe-queue/exchange/apply` | `{bundle, preview_digest, confirmed: true}` 重新校验后写回 |

文件和接口请求上限 64 MiB；一个结果包最多 100 项。清单和结果版本为 1；技术字段复用现有 `ProbeResult` 的 Go JSON 字段名，用户不需要手工构造它们。

## 构建与验证

CLI 源码入口为 `cmd/media-probe-worker`。普通 MSG 镜像额外包含 `/usr/local/bin/media-probe-worker`，不会因包含此程序而启动任务；将来独立节点运行时明确覆盖 entrypoint 并关闭服务器健康检查，不启动 MSG 服务。候选镜像仍按精确 SHA 和不可变 digest 构建、验证，当前尚未发布。

本地定向测试：

```text
go test ./internal/service -run "^TestProbe(Exchange|Worker)" -count=1
go test ./internal/handler -run "^TestProbeExchange" -count=1
go test ./cmd/media-probe-worker -count=1
```

这些测试使用合成记录、本机 HTTP 服务和模拟探测器，覆盖请求/响应体预算、去重、共享命中/异常、鉴权不重放、映射冲突、取消、检查点、预览失效、导入幂等与审计。真实 B 账号、转存规则、当前 CDN 返回格式及真实 ffprobe 链路须在获得相应执行许可后单独验证，不能把离线通过当作真实链路已验证。
