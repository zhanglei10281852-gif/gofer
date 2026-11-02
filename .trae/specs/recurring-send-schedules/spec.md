# 循环预约发送（每日 / 每周计划）- 产品需求文档

## Overview
- **Summary**: 把写信窗口现有的「单次预约发送」扩展为可选的每日或每周循环计划：用户指定时区、本地时刻、开始日期、每周发送日以及可选的结束日期；系统按指定时区的本地日历逐次产生彼此独立的邮件实例，并提供下次发送预览、各次结果、编辑未来内容、暂停、恢复、结束等管理能力。不选择重复时行为与现有单次预约完全一致。
- **Purpose**: 每周固定提醒（周报、例会提醒等）场景下，用户不再需要每周复制草稿、重新预约日期；同时通过实例级不可变快照、时区日历枚举、停机/夏令时语义和崩溃幂等保证投递正确性与内容安全。
- **Target Users**: gofer Webmail 的所有写信用户（个人用户与自建实例用户）。

## Goals
- 写信窗口的 Send later 面板支持「不重复 / 每日 / 每周」三种选择及完整计划参数。
- 计划保存后内容独立于原草稿：草稿的再编辑或删除、某次发送成功、常规文件清理均不影响未来邮件的正文与附件。
- 每个应发送的本地日期对应独立的邮件身份（Message-ID）、独立发送快照与独立结果；重试只使用该次快照；计划的后续编辑不能改动已产生的发送。
- 日期严格按用户指定时区的本地日历推进；正确处理夏令时跳过（gap）与重复（ambiguous）本地时刻。
- 服务停机错过多个日期时，恢复后只发送最近一个已到期日期，更早的明确记为跳过；不集中补发；周期推进不以重试时间为起点。
- 支持暂停 / 恢复 / 结束计划：只阻止尚未冻结（产生快照）的发送；恢复不补发暂停期间日期；已进入投递或等待人工处理的实例保留各自结果与内容。
- 临时失败沿用现有发送队列重试规则；提交后失去结果（ambiguous）保持不确定状态，仅用户明确确认才可重发；后续日期独立执行。
- 实例创建失败、重复唤醒、创建成功后进程退出都不得使同一日期重复产生发送。
- 计划与实例状态在页面刷新与服务重启后一致可查。
- 结束计划后，仅当不存在待处理实例引用时才释放其内容文件。
- 现有即时发送、单次预约、发件身份（identity SMTP）与 Sent 副本恢复继续正常工作。

## Non-Goals
- 不引入 iCalendar/RFC 5545 RRULE 全集（不支持每月、每年、自定义间隔、例外日期、节假日历）。
- 不做按收件人差异化的循环内容（所有实例共享同一模板，仅 Message-ID/Date 每次独立）。
- 不改动现有单次预约、即时发送、日历邀请回复（calendar reply/notification）的投递链路。
- 不提供循环邮件的远端（Gmail/Outlook/JMAP）原生调度，统一由 gofer 本地投递。
- 不做计划的导入导出、多用户共享计划。

## Background & Context
- 现有单次预约链路：`POST /compose/schedule`（[handler.go](file:///f:/swe/098872/project-04/internal/handler/handler.go#L5917-L5985)）先保存草稿（`messages` 行 + Drafts 文件夹状态），再以 `outgoing_sends` 行持久化完整 MIME（`mime_data`）与消息快照（`message_json`），`is_scheduled=1`、`status=pending`，对 `message_id` 唯一；到期由 worker 领取投递（[outgoing.go](file:///f:/swe/098872/project-04/internal/handler/outgoing.go)）。
- 投递有两条执行路径，均基于同一 storage 队列：共享/单库的 `StartOutgoingSendWorker` 与按用户路由的 `userMailDelivery.Run`（[user_mail_delivery.go](file:///f:/swe/098872/project-04/internal/handler/user_mail_delivery.go)，`ClaimDueOutgoingSendsForAccount`）。
- 草稿再编辑通过 `PublishDraft` 的 `Pending` 负载在同一事务内原子更新等待中的单次快照（[draft_publication.go](file:///f:/swe/098872/project-04/internal/storage/draft_publication.go#L110-L121)）；丢弃草稿会取消其 pending/failed/ambiguous 的单次发送。
- 附件为 blob 文件；常规清理（24h）由 `WithUserBlobReferences` 汇总引用路径决定保留集，已把未取消且未彻底完成的 `outgoing_sends.message_json` 中附件路径纳入保留（[user_blob_references.go](file:///f:/swe/098872/project-04/internal/storage/user_blob_references.go#L143-L179)）。
- 不确定状态已有完整机制：SMTP/供应商 ambiguous → `OutgoingSendAmbiguous`；重启时 `MarkInterruptedOutgoingSendsAmbiguous`；重试需 `confirm_ambiguous` 显式确认（[outgoing.go](file:///f:/swe/098872/project-04/internal/storage/outgoing.go#L201-L236)）。
- Schema 当前版本 133（[storage.go](file:///f:/swe/098872/project-04/internal/storage/storage.go#L60)），新增表/列需 v134 迁移并同步更新 `schema.sql`。
- Scheduled 统一文件夹目前是 `messages JOIN outgoing_sends` 的 pending 预约草稿视图（[repository.go](file:///f:/swe/098872/project-04/internal/storage/repository.go#L6219-L6225)），侧栏角标走 `GetFolderEmailCountFilteredForUser`。

## Functional Requirements

### 计划创建
- **FR-1**: Send later 面板提供重复方式选择：不重复（none）、每日（daily）、每周（weekly）。
- **FR-2**: 选择 daily/weekly 时，用户必须能指定：IANA 时区、本地时刻（小时 + 分钟，沿用 5 分钟粒度）、开始日期；weekly 还需选择每周发送日（周一至周日至少 1 天）；结束日期可选。默认时区取用户界面设置时区，再退回浏览器上报时区。
- **FR-3**: 服务端校验：时区可加载；时刻合法；开始日期的本地时刻必须晚于当前至少 60 秒（与现单次规则一致）；weekly 星期集合非空；结束日期不早于开始日期；不重复时拒绝循环参数。
- **FR-4**: 计划保存服务端事务完成：持久化计划行（规则、状态 active、当前内容 JSON）、把附件复制到计划自有 blob 目录并改写快照路径，然后删除用于创建的原始草稿（含远端草稿删除排队），但计划不因此被取消。
- **FR-5**: 不选择重复时，请求与持久化结果与今天完全一致（一条草稿 + 一条 `outgoing_sends`，`is_scheduled=1`），不产生计划与实例行。

### 实例化与投递
- **FR-6**: 每个「应发送的本地日期」对应一个计划实例（`send_schedule_instances` 行），键为 `(schedule_id, local_date)` 唯一；实例冻结时生成全新 Message-ID 与 Date，并从计划当前内容复制生成该实例自有的 `outgoing_sends` 行（自带 MIME 与快照，`message_id` 为空、`draft_id` 为空，带 `schedule_instance_id` 关联）。
- **FR-7**: 实例只在其本地时刻到期时由 worker 冻结（惰性冻结），不提前产生快照；冻结后该实例的投递、重试、Sent 副本完全复用现有 `outgoing_sends` 机制。
- **FR-8**: 实例状态随关联 outgoing send 对账（pending/sending/sent/failed/ambiguous/canceled/skipped），实例行冗余保存状态、错误、次数、时间戳，使其在关联行被留存清理删除后仍可查。
- **FR-9**: 计划 worker 与现有投递 worker 同节拍运行（共享 30s ticker 与按用户 WakeMutations 唤醒），在领取到期发送之前执行到期实例化；纯本地计算与事务，无网络依赖。

### 时区与日历语义
- **FR-10**: 日期按计划时区的本地日历推进：daily 每个日历日一个候选；weekly 仅所选星期的日历日候选；候选时刻 = 该日本地 HH:MM。
- **FR-11**: 候选本地时刻因夏令时跳过时（Go 归一化后墙钟字段不符），该日期产生 `skipped`（`dst_gap`）实例，不发送。
- **FR-12**: 候选本地时刻因夏令时回退出现两次时，以确定性规则解析为唯一一个绝对时刻（取较早的一次），该日期只产生一封。
- **FR-13**: 停机恢复语义：一次枚举发现多个已到期且尚无实例的日期时，仅最新一个冻结为真实发送，其余全部记 `skipped`（`missed`）；正常 tick 下通常 0 或 1 个到期。下一周期只由本地日历与持久化游标决定，与 `next_attempt_at` 等重试时间无关。
- **FR-14**: 计划在暂停期间被枚举时，到期候选只产生 `skipped`（`paused`）实例，不发送；服务停机跨越暂停期时，恢复后（仍暂停或已恢复）这些日期同样不得补发。
- **FR-15**: 枚举使用计划行上持久化的只进游标（下一个待考虑本地日期），避免长区间重复扫描；设置/状态变更只影响游标之后尚未产生的日期。

### 计划管理
- **FR-16**: 用户可查看计划列表：规则摘要、时区与本地时刻、下次发送绝对时间（无下次时说明原因：已暂停/已结束/已到结束日期）、当前状态。
- **FR-17**: 用户可查看单个计划的各次实例结果：本地日期、计划时刻、状态（sent/failed/retrying/ambiguous/skipped 及原因）、错误信息、尝试次数、时间；实例结果不因刷新丢失。
- **FR-18**: 用户可编辑计划设置（时刻、时区、星期、结束日期、开始日期）：仅影响游标之后尚未冻结的日期；已存在实例（含 skipped）永不被改写或补发。
- **FR-19**: 用户可编辑未来内容（收件人、抄送/密送、主题、正文、附件、发件身份）：替换计划当前内容与计划自有附件文件；已冻结实例与进行中的重试使用各自快照，不受影响。
- **FR-20**: 暂停计划：阻止游标之后新实例冻结；不取消任何已冻结实例（pending/sending/failed/ambiguous 保持各自状态、重试与人工处理入口）。
- **FR-21**: 恢复计划：从恢复时点按日历继续；暂停期间到期日期不补发（已由 FR-14 记录 skipped）。
- **FR-22**: 结束计划：阻止未来实例；已产生实例全部保留结果与内容并可继续重试/确认重发；超过结束日期且无未来候选的计划由 worker 自动置为 ended（completed）。
- **FR-23**: 实例的失败重试与 ambiguous 确认重发沿用现有 `/api/outgoing-sends/{id}/retry` 语义（ambiguous 必须显式 confirm，提示可能已投递）；任何单实例状态不影响其他日期实例的独立执行。

### 内容与文件生命周期
- **FR-24**: 计划创建当日即把正文（text/HTML 内嵌于内容 JSON）与附件字节复制到计划自有存储；此后原草稿的编辑、删除（含远端草稿）、任何一次发送成功（含其 Sent 副本处理与草稿清理路径）都不改变或删除未来实例所需内容。
- **FR-25**: 常规文件清理的引用保留集必须包含：active/paused 计划当前内容引用的附件路径；ended 计划在仍存在 pending/sending/failed/ambiguous 实例期间引用的附件路径。
- **FR-26**: 计划 ended 且其所有实例均已终结（sent/canceled/skipped，或失败/不确定已处理至终态）后，其内容附件文件可被常规清理删除（也可在结束操作时尽力立即删除）；内容 JSON 中的文本与元数据保留以支持历史展示。
- **FR-27**: 编辑未来内容替换附件后，旧版本文件在无任何未终结实例引用时由清理回收；已冻结实例依赖的是其 `outgoing_sends` 内嵌 MIME/快照，不依赖计划当前文件。

### 可靠性与一致性
- **FR-28**: 冻结在单个数据库事务内完成：构造实例行与 `outgoing_sends` 行并推进游标；`(schedule_id, local_date)` 唯一约束 + 事务回滚保证重复唤醒、构造失败、进程在提交后退出都不会为同一日期产生两封邮件。
- **FR-29**: 服务重启后：计划状态、游标、实例与结果完整可查；投递中（sending）的发送沿用现有 interrupted→ambiguous 恢复；实例对账纠正其镜像状态；materializer 依据持久化游标继续，不重复冻结。
- **FR-30**: 所有计划/实例/内容的读写都限定在所属账户用户范围内（路由存储与 accounts JOIN 校验），与现有 outgoing 端点的归属检查一致。

## Non-Functional Requirements
- **NFR-1（回归兼容）**: 现有即时发送、单次预约、identity SMTP 发件、Gmail/Outlook/JMAP 发送、Sent 副本（含 ambiguous 对账恢复）行为与测试保持通过；新表新列默认空值，不改变旧数据语义。
- **NFR-2（可测性）**: 时区枚举（gap/ambiguous）、停机补最近一个、暂停/结束边界、幂等冻结均以纯函数或 storage 级单元测试覆盖，使用固定时钟与可构造时区（如 America/New_York、Asia/Kolkata、UTC）。
- **NFR-3（性能）**: 单次枚举有界（单次 tick 每个计划处理候选数设上限，如 ≤ 400 个本地日）；计划列表/详情查询走索引；不引入每实例远期预生成。
- **NFR-4（安全）**: 计划内容、附件与结果遵循现有归属模型；文件路径仅来自服务端生成位置；模板输出沿用现有 HTML 消毒。
- **NFR-5（可观察）**: 关键路径（冻结、跳过、暂停错过、自动结束、内容释放）输出与现有风格一致的日志；前端通过既有 SSE `EventSendResult` / mutation 机制与轮询刷新计划状态。

## Constraints
- **Technical**: Go + SQLite（WAL，单文件 per-user 库）；schema v134 迁移（新表 + `outgoing_sends.schedule_instance_id` 列与索引）；前端为 templ + 原生 JS（app.js action 注册模式）+ selectbox/calendar/popover 既有组件；MIME 构造复用 `buildOutgoingMIME`/`message.BuildMIMEMessage*`；投递复用 `outgoing_sends` 两条执行路径。
- **Business**: 不破坏 v133 数据库升级；单实例部署语义（与现有队列相同的单 worker/单租约假设）。
- **Dependencies**: IANA tzdata（Go 内置）；无新增第三方依赖。

## Assumptions
- 「每周固定提醒」仅需 weekly 的星期多选 + daily；结束日期含当天（当天满足星期仍发送）。
- 「编辑未来内容」入口为计划详情中打开预填的写信窗口（同 compose 组件），保存写回计划而非草稿。
- 暂停期间错过的日期保留 `skipped(paused)` 记录，以便「各次结果」完整且恢复语义无歧义。
- 实例历史长期保留；其关联 `outgoing_sends` 行按现有留存策略老化后，实例行自身的冗余状态仍支撑结果展示。
- 未来内容模板不保存线程头（in_reply_to/references 为空），循环邮件每次为独立新邮件；若从带线程头的草稿创建计划，仅保存主题/正文/收件人/附件/发件身份。
- 计划只属于一个发件账户（创建时选定）；identity SMTP 等发件方式按冻结时账户仍存在的身份解析，身份被删除时按现有单次预约失败路径处理（该实例 failed，可重试，不影响后续日期）。

## Acceptance Criteria

### AC-1: 不重复时仍是单次预约
- **Type**: `rule`
- **Given**: 用户在 Send later 面板保持 repeat=none 并选择未来日期时刻提交
- **When**: 请求 `POST /compose/schedule`
- **Then**: 与改造前完全相同：生成一条草稿与一条 `outgoing_sends`（`is_scheduled=1`、`status=pending`），不产生 `send_schedules`/`send_schedule_instances` 行
- **Pass Condition**: 存储断言无计划行；Scheduled 列表、取消、到期投递、草稿联动行为与既有测试一致
- **Evidence**: `go test ./internal/handler/ ./internal/storage/` 中单次预约相关测试；新增回归测试断言计划表为空

### AC-2: 计划参数提交与校验
- **Type**: `rule`
- **Given**: 用户填写 repeat=daily|weekly、时区、时刻、开始日期、weekly 的星期集合、可选结束日期
- **When**: 提交 `POST /compose/schedule`
- **Then**: 合法参数创建 active 计划；非法/缺失参数（未知时区、非 5 分钟整点、开始时刻不足 60 秒未来、weekly 无星期、结束日期早于开始）返回 400 且不产生任何计划/草稿残留
- **Pass Condition**: 每组非法参数得到 400，库中无对应计划行；合法请求返回计划标识与下次发送时间
- **Evidence**: handler 层表单校验测试（表驱动）

### AC-3: 计划内容独立于原草稿
- **Type**: `rule`
- **Given**: 一个已保存的每日计划（含附件）
- **When**: 用户随后编辑或删除其原始草稿（本地 + 触发远端草稿删除），并执行常规文件清理
- **Then**: 计划当前内容 JSON 与计划自有附件文件仍完整；下一次到期实例的 MIME 正文与附件字节与创建时一致
- **Pass Condition**: 草稿删除后计划行仍在；清理后计划附件路径存在；冻结实例的 `mime_data` 可被完整解析出全部附件
- **Evidence**: storage/handler 测试：删草稿 → cleanup（`WithUserBlobReferences`）→ 冻结并解析 MIME

### AC-4: 每个本地日期独立身份与独立快照
- **Type**: `rule`
- **Given**: active 每日计划经历若干次到期
- **When**: worker 逐次冻结并投递
- **Then**: 每个本地日期恰有一个实例行；每个实例关联独立 `outgoing_sends`，Message-ID 两两不同、Date 为该实例绝对时刻；任一实例重试不改变其 Message-ID/Date 与 MIME
- **Pass Condition**: 连续 3 个实例的 Message-ID 唯一；实例重试后快照字节与首次冻结一致（除既有 attempt 列外）
- **Evidence**: materializer + 投递存储测试

### AC-5: 计划编辑不影响已冻结实例
- **Type**: `rule`
- **Given**: 计划已有一个 frozen（pending/sending/sent/failed/ambiguous）实例
- **When**: 用户修改计划设置或编辑未来内容
- **Then**: 已冻结实例的 `outgoing_sends.message_json`/`mime_data` 与实例行不被更新；仅游标之后的未来日期使用新规则/新内容
- **Pass Condition**: 编辑前后对既有实例快照做字节比较不变；下一实例反映新主题/正文/附件
- **Evidence**: storage 测试 + 内容编辑 handler 测试

### AC-6: 时区日历推进与 weekly 星期过滤
- **Type**: `rule`
- **Given**: 不同时区、daily/weekly、含/不含结束日期的规则
- **When**: 以固定时钟枚举候选日期
- **Then**: daily 逐日本地日期；weekly 仅命中所选星期；结束日期之后无候选且计划可自动 ended；枚举结果只依赖时区日历与游标
- **Pass Condition**: 表驱动枚举测试（UTC、Asia/Kolkata +05:30、America/New_York）断言日期序列与绝对时刻
- **Evidence**: 纯函数单元测试（新 internal 包或 storage 包内）

### AC-7: 夏令时 gap 记为跳过
- **Type**: `rule`
- **Given**: America/New_York 2026-03-08 本地 02:30 的 daily 计划（该墙钟不存在）
- **When**: worker 枚举该到期日
- **Then**: 产生且仅产生一个实例，`status=skipped`、`skip_reason=dst_gap`，不产生 `outgoing_sends`，不投递
- **Pass Condition**: 实例行存在且 skipped/dst_gap；outgoing 表无对应行；次日正常产生发送实例
- **Evidence**: 枚举/物化测试

### AC-8: 夏令时重复时刻只发一封
- **Type**: `rule`
- **Given**: America/New_York 2026-11-01 本地 01:30 的 daily 计划（该墙钟出现两次）
- **When**: worker 枚举该到期日并重复唤醒多次
- **Then**: 该日期只有一个实例与一封 `outgoing_sends`，解析为确定的唯一绝对时刻（较早的一次，EDT 05:30Z）
- **Pass Condition**: 实例计数 1；outgoing 计数 1；scheduled_at 等于期望 UTC 时刻
- **Evidence**: 物化测试（多次 tick）

### AC-9: 停机恢复只补最近一个到期日
- **Type**: `rule`
- **Given**: daily 计划在服务停机期间跨过 3 个应发送本地日期且无实例
- **When**: 服务恢复、worker 枚举
- **Then**: 最早两个日期为 `skipped(missed)`，最新一个日期冻结为 pending 发送；游标推进到最新日期之后；不存在批量补发；后续周期仍按日历推进（与重试时间无关）
- **Pass Condition**: 实例状态精确为 [missed, missed, pending→sent]；再下一个到期日为次日而非「最新重试时间之后一天」
- **Evidence**: storage 物化测试，固定时钟跨 3 天

### AC-10: 暂停/恢复语义
- **Type**: `rule`
- **Given**: active 计划存在一个已冻结 pending 实例；用户暂停
- **When**: 暂停期间多个日期到期（含进程停机后恢复），随后用户恢复
- **Then**: 暂停期到期日期为 `skipped(paused)`；已冻结实例状态/快照不变并继续其重试/人工入口；恢复后首个到期日按恢复后的日历发送，不补发暂停期任何日期
- **Pass Condition**: 暂停期无新冻结发送；恢复后序列从当天/未来继续；旧实例仍可重试
- **Evidence**: 物化 + 状态机测试

### AC-11: 结束语义与内容释放门槛
- **Type**: `rule`
- **Given**: 用户结束一个仍有未终结实例（如 failed/ambiguous/pending）的计划
- **When**: 结束后执行文件清理
- **Then**: 未来不再产生实例；既有实例保留全部结果与内容且附件路径仍被清理保留集保护；当全部实例终结后再次清理，计划附件文件被删除（内容 JSON 文本保留）
- **Pass Condition**: 第一次清理后文件存在、实例可重试；全部终结后清理文件不存在；实例历史仍可展示
- **Evidence**: `WithUserBlobReferences` + materializer 测试

### AC-12: 失败重试与 ambiguous 人工确认
- **Type**: `rule`
- **Given**: 某实例首次投递遇到临时失败；另一实例提交后失去结果（ambiguous）
- **When**: 队列重试与用户操作发生
- **Then**: 临时失败沿用现有退避与 12 次上限，使用该实例同一快照；ambiguous 实例保持不确定状态，仅 `confirm=true` 的重试可重新投递；两者都不改变其他日期实例
- **Pass Condition**: next_attempt 退避序列与现网一致；未带 confirm 的 ambiguous 重试返回 409；相邻日期实例独立投递
- **Evidence**: 复用现有重试测试 + 计划场景断言

### AC-13: 幂等冻结与崩溃安全
- **Type**: `rule`
- **When**: 同一到期日发生重复唤醒、冻结事务中途失败、或实例与 outgoing 行创建提交后进程立即退出
- **Then**: 任何情况下 `(schedule_id, local_date)` 至多一个实例与一封 outgoing send；重启后对账不产生重复发送
- **Pass Condition**: 并发/重复调用物化后计数恒为 1；模拟提交后重启（重新打开 DB + 恢复流程）无重复行，到期邮件仅投递一次
- **Evidence**: storage 并发测试 + 重启恢复测试

### AC-14: 刷新与重启后状态一致
- **Type**: `rule`
- **Given**: 处于各状态的计划与实例（active/paused/ended；pending/sending/sent/failed/ambiguous/skipped）
- **When**: 页面刷新或服务重启
- **Then**: 计划列表/详情与实例结果返回与重启前一致；sending 中的发送被标记 ambiguous 且实例镜像同步；下次发送时间由规则与游标重新算出且正确
- **Evidence**: 重启式存储测试；API 层列表/详情测试
- **Pass Condition**: 重启前后状态集合一致；sending→ambiguous 镜像更新；next 发送正确

### AC-15: 计划与实例的归属隔离
- **Type**: `rule`
- **Given**: 两个不同用户的计划
- **When**: 交叉访问列表/详情/控制端点或 worker 按账户物化
- **Then**: 只能读写本人账户的计划与实例；跨用户访问为 404/403
- **Pass Condition**: 交叉请求均失败且无数据泄露；按用户物化只处理该账户计划
- **Evidence**: handler 归属测试

### AC-16: 既有能力回归
- **Type**: `rule`
- **When**: 完成改造后运行与发送相关的既有测试套件
- **Then**: 即时发送、单次预约（含取消/立即发送/草稿联动）、identity SMTP、Gmail/Outlook/JMAP 发送、Sent 副本（含 ambiguous 对账）、迁移测试全部通过
- **Pass Condition**: `go build ./...` 与 `go test ./internal/...` 通过（既有失败需甄别为与本变更无关）
- **Evidence**: 完整命令输出

### AC-17: 计划管理界面可用性
- **Type**: `rubric`
- **Dimension**: 端到端交互完整性：面板重复选项/时区/星期/结束日期；Scheduled 中计划卡片展示下次发送与状态；详情查看各次结果；编辑未来内容；暂停/恢复/结束；ambiguous 确认提示
- **Scale**: 1-5
- **Anchors**: 1 = 仅 API 可用或界面残缺；3 = 主要操作可完成但有明显跳转/状态不一致；5 = 全流程在页面内闭环、状态实时、文案清晰、不重复发送风险有明确提示
- **Pass Threshold**: >= 4
- **Evidence**: 浏览器走查（创建 daily/weekly 计划 → 查看下次/结果 → 编辑内容 → 暂停/恢复 → 结束）截图与操作记录

### AC-18: 实现与测试质量
- **Type**: `rubric`
- **Dimension**: 时区/状态机/幂等核心逻辑的测试覆盖与代码可维护性（纯函数边界、复用现有队列、迁移与回滚安全、日志一致）
- **Scale**: 1-5
- **Anchors**: 1 = 核心逻辑仅经手工验证；3 = 主路径有测试但边界（DST/停机/崩溃）缺失；5 = 边界全部有自动化测试且实现分层清晰
- **Pass Threshold**: >= 4
- **Evidence**: 新增测试清单与覆盖率说明、独立评审结论

## Open Questions
- 无阻塞性问题。以下采用上述 Assumptions 中的默认决策，如不认可可在审批时指出：
  - [ ] 暂停期间错过的日期记录为 `skipped(paused)`（而非不留痕）。
  - [ ] 结束日期含当天。
  - [ ] 计划邮件不沿用草稿线程头，每封均为独立新邮件。
