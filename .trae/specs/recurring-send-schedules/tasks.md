# 循环预约发送（每日 / 每周计划）- 实施计划

## 设计要点（实现约束）

### 数据模型（schema v134）
- `send_schedules`：`id, account_id, status('active'|'paused'|'ended'), recurrence('daily'|'weekly'), timezone, hour, minute, start_date(YYYY-MM-DD), end_date(''或YYYY-MM-DD), weekdays(weekly 用 '0'..'6' 逗号集合，0=周日), content_json(outgoingMessageSnapshot 模板，MessageID/Date 为空), cursor_date(只进游标，下一待考虑本地日期), next_occurrence_at(下次绝对时刻，可为 NULL；驱动按用户队列唤醒), ended_reason(''|'user'|'completed'), paused_at, ended_at, created_at, updated_at`。
- `send_schedule_instances`：`id, schedule_id, local_date(YYYY-MM-DD), scheduled_at(UTC), status('pending'|'sending'|'sent'|'failed'|'ambiguous'|'canceled'|'skipped'), skip_reason(''|'dst_gap'|'missed'|'paused'), outgoing_send_id, last_error, attempt_count, sent_at, created_at, updated_at`；`UNIQUE(schedule_id, local_date)`。
- `outgoing_sends` 增加 `schedule_instance_id TEXT NOT NULL DEFAULT ''` 与索引；实例发送 `message_id=NULL, draft_id='', is_scheduled=1`（NULL 不触发 UNIQUE(message_id) 冲突）。

### 关键机制
- **惰性冻结**：实例仅在本地时刻到期时由物化器创建；不预生成未来实例。「下次发送」由计划规则+游标实时计算并冗余到 `next_occurrence_at`。
- **物化单事务**：storage 层 `MaterializeDueScheduleInstances(ctx, accountID, now, build)` 在单个 IMMEDIATE 事务内枚举候选、写 skipped 行、对最新到期日调用注入的纯 CPU `build(occ)→PreparedSend{transport, envelopeFrom, recipients, mimeData, messageJSON}`、同事务插入 instance+outgoing_sends、推进游标、维护 next_occurrence_at、自动 ended。`(schedule_id,local_date)` 唯一 + 事务回滚保证幂等。
- **枚举语义**：active 计划一批多个到期 → 仅最新一个冻结，其余 `skipped(missed)`；paused 计划到期 → `skipped(paused)`；gap → `skipped(dst_gap)`；ambiguous → Go `time.Date` 确定的较早时刻，一封。恢复(resume)在同一事务内先把 paused 期间到期补记 `skipped(paused)` 再置 active。
- **对账**：`ReconcileScheduleInstances` 用一条 UPDATE...FROM 以关联 outgoing send 状态镜像实例（含 attempt/error/sent_at）；send 行被留存删除后镜像值保持不变。
- **内容独立**：创建计划时把附件复制到计划自有 blob 目录 `<base>/<account>/schedules/<scheduleID>/<nonce>/`，再删除原草稿；编辑内容整目录替换。blob 引用保留集新增 send_schedules.content_json 路径（active/paused 始终保留；ended 仅在存在未终结实例时保留）。
- **投递复用**：实例 outgoing_sends 行沿用两条既有投递路径、重试退避、ambiguous 确认、Sent 副本；DraftID 为空使 `cleanupDeliveredDraft` 自然空转；`saveSentMessageSnapshot`/`cacheSent` 按 Message-ID upsert Sent 记录照常工作。
- **唤醒**：全局 worker tick 增加按账户物化；按用户路由把 `send_schedules.next_occurrence_at` UNION 进 `NextAccountMailQueueAttempt`，并在 `userMailDelivery.Run` 领取前先化物化+对账。

## Task 1: 纯函数 recurrence 引擎（时区日历/DST）
- **Status**: `pending`
- **Priority**: high
- **Depends On**: None
- **Description**:
  - 新建 `internal/recurrence/recur.go`：`Rule{Recurrence, Timezone, Hour, Minute, StartDate, EndDate, Weekdays}`、`CivilDate`、`Occurrence{Date, InstantUTC, Gap, Ambiguous}`；方法 `Location()`、`MatchesDate(CivilDate)`、`Resolve(CivilDate,*time.Location) Occurrence`（gap 检测=归一化后墙钟字段不符；ambiguous=解析前后偏移变化，统一取较早绝对时刻）、`NextMatch(from CivilDate) (CivilDate,bool)`（按结束日期截断）、`NextOccurrence(from,now)` 供下次发送计算。
  - 不依赖 storage/handler，无网络、无 I/O。
- **Acceptance Criteria Addressed**: AC-6, AC-7, AC-8, AC-18
- **Test Requirements**:
  - `rule` TR-1.1: 表驱动测试覆盖 UTC/Asia/Kolkata(+05:30)/America/New_York 的 daily 与 weekly（含星期过滤）序列、结束日期截断；证据 `go test ./internal/recurrence/`。
  - `rule` TR-1.2: America/New_York 2026-03-08 02:30 解析为 Gap=true；2026-11-01 01:30 解析为 Ambiguous=true 且 InstantUTC=05:30Z（较早一次），同一日期重复解析结果恒定；证据测试断言。
  - `rule` TR-1.3: 非法时区名返回错误；结束日期早于开始时 NextMatch 永远为空。
- **Notes**: weekday 编码 0=Sunday…6=Saturday，与 JS `getDay()` 一致，前后端共享同一语义。

## Task 2: schema v134 迁移与 schema.sql
- **Status**: `pending`
- **Priority**: high
- **Depends On**: None
- **Description**:
  - `schema.sql` 新增 `send_schedules`、`send_schedule_instances` 建表与上述索引；`outgoing_sends` 增加 `schedule_instance_id` 列与 `idx_outgoing_sends_schedule_instance`。
  - 新增 `migrateV133ToV134(tx)`（CREATE TABLE IF NOT EXISTS + `ALTER TABLE outgoing_sends ADD COLUMN`，仿照既有幂等迁移），storage.go 接入迁移链，`CurrentSchemaVersion=134`。
- **Acceptance Criteria Addressed**: AC-14, AC-16
- **Test Requirements**:
  - `rule` TR-2.1: 新建库版本=134 且表/列/索引存在；从 133 旧库升级后版本=134、既有 outgoing_sends 行 `schedule_instance_id=''` 且既有发送测试全部通过；证据新建 `send_schedule_migration_test.go`。
- **Notes**: ALTER 前用 PRAGMA table_info 风格检查或仿照既有 ADD COLUMN 迁移的容错写法。

## Task 3: 计划内容 blob 存储与清理引用保护
- **Status**: `pending`
- **Priority**: high
- **Depends On**: Task 2
- **Description**:
  - `internal/store/blob.go`：新增 `schedulesDir(accountID, scheduleID)`、`NewScheduleVersion(accountID, scheduleID)`、`StoreScheduleAttachment(...)`、`DeleteSchedule(accountID, scheduleID)`（整目录，容忍不存在），布局 `<base>/<account>/schedules/<id>/<nonce>/attachments/...`。
  - `internal/storage/user_blob_references.go`：保留集新增查询——`send_schedules.content_json` 中附件路径，条件为 `status IN ('active','paused') OR EXISTS(该计划未终结实例 status IN pending/sending/failed/ambiguous)`；JSON 解析失败按现有「retained payload 格式错误即失败」处理。
- **Acceptance Criteria Addressed**: AC-3, AC-11, AC-16
- **Test Requirements**:
  - `rule` TR-3.1: store 层附件写入/删除测试（计划目录隔离、版本目录隔离）。
  - `rule` TR-3.2: active/paused 计划附件在 CleanupUserFiles 后保留；ended 且全部实例终结后被清理；ended 但有 failed 实例时保留；证据扩展 `user_blob_references_test.go`。

## Task 4: storage 层计划 CRUD
- **Status**: `pending`
- **Priority**: high
- **Depends On**: Task 2, Task 3
- **Description**:
  - 新文件 `internal/storage/send_schedule.go`：`SendSchedule`/`SendScheduleInstance`/`SendScheduleSummary` 结构体；`CreateSendSchedule(ctx, input)`（含 content_json、游标=start_date、next_occurrence_at 初始计算）；`GetSendScheduleForUser`、`ListSendSchedulesForUser`（JOIN accounts 归属；附 next/状态/未终结实例计数）、`GetSendScheduleInstancesForUser`（按 local_date 倒序分页）。
  - `UpdateSendScheduleSettings`（规则字段；只影响未来，不动实例/游标不回退；重算 next_occurrence_at）、`UpdateSendScheduleContent`（替换 content_json）、`PauseSendSchedule`、`ResumeSendSchedule`（同一事务内：由注入的 recurrence 计算辅助把暂停期到期候选写成 skipped(paused)+推进游标，再置 active；见 Task 5 共用枚举函数）、`EndSendSchedule(reason)`。
  - 所有用户入口方法均 `account_id IN (SELECT id FROM accounts WHERE user_id=?)` 校验。
- **Acceptance Criteria Addressed**: AC-5, AC-14, AC-15, AC-11
- **Test Requirements**:
  - `rule` TR-4.1: CRUD 归属测试：跨用户读/改得到 sql.ErrNoRows/0 行。
  - `rule` TR-4.2: settings 更新不改动既有实例行；content 更新只替换 content_json；pause/resume/end 状态转换合法路径与非法路径（ended 不可 resume）。
  - `rule` TR-4.3: resume 事务把暂停期已到期候选补记 skipped(paused) 且不产生 outgoing_sends（与 Task 5 共用函数，测试可在 Task 5 完成后补全）。

## Task 5: storage 物化器、对账与自动结束
- **Status**: `pending`
- **Priority**: high
- **Depends On**: Task 1, Task 2, Task 4
- **Description**:
  - 定义 `ScheduleSendBuilder func(contentJSON []byte, localDate string, instant time.Time) (PreparedScheduleSend, error)` 与 `PreparedScheduleSend{Transport, EnvelopeFrom, EnvelopeRecipients, MIMEData, MessageJSON, InstanceID}`（InstanceID 由 storage 预生成 uuid 传入，builder 据此写新 Message-ID/Date）。
  - `MaterializeDueScheduleInstances(ctx, accountID string, now time.Time, build ScheduleSendBuilder) (report, error)`：IMMEDIATE 单事务；按计划用 recurrence 引擎从 cursor_date 枚举到今日（每计划候选上限 400）；写 dst_gap/missed/paused skipped 行（INSERT OR IGNORE）；active 最新到期调 build（builder 报错 → 实例 failed+last_error 并推进；JMAP 未启用类不可构建情形由调用方在 build 中返回正常 pending 载荷，claim 侧已有过滤）；插入 outgoing_sends（显式 uuid、schedule_instance_id、send_after=instant、next_attempt=CURRENT_TIMESTAMP）与关联实例；推进游标；重算 next_occurrence_at；end_date 耗尽且无未来候选 → ended/completed。
  - `ReconcileScheduleInstances(ctx, accountID)`：UPDATE...FROM 镜像 outgoing_sends 状态/attempt/error/sent_at（仅当不同），不动 skipped 行与无 send 行的历史行。
  - resume 复用同一「paused 标记」枚举路径（抽出计划内枚举函数，参数 allowFreeze bool）。
- **Acceptance Criteria Addressed**: AC-4, AC-5, AC-6, AC-7, AC-8, AC-9, AC-10, AC-13, AC-14
- **Test Requirements**:
  - `rule` TR-5.1: 固定时钟：停机跨 3 个到期日 → 两 skipped(missed)+1 pending；再推进一天 → 次日实例；游标与 next_occurrence_at 正确。
  - `rule` TR-5.2: gap 日 → skipped(dst_gap) 无 outgoing；ambiguous 日 → 单实例单发送且时刻=较早一次；重复调用物化（模拟重复唤醒）计数恒为 1。
  - `rule` TR-5.3: paused 期间到期全部 skipped(paused)；resume 后不补发；已 pending 实例不受 pause 影响。
  - `rule` TR-5.4: builder 失败 → 实例 failed 且游标推进，下一日正常；build panic/error 不留下半截 outgoing 行（事务回滚断言）。
  - `rule` TR-5.5: 模拟「插入提交后进程退出」：重新打开 DB 后直接对账+再物化，无重复行、无重复发送。
  - `rule` TR-5.6: end_date 最后一个候选处理完后计划自动 ended/completed；之后物化为空操作。
  - `rule` TR-5.7: 对账：send 行 sending→sent/failed/ambiguous/canceled 镜像正确；删除 send 行后实例镜像保留。

## Task 6: outgoing_sends 关联实例列贯通
- **Status**: `pending`
- **Priority**: high
- **Depends On**: Task 2
- **Description**:
  - `OutgoingSend`/Summary/select/scan/`QueueOutgoingSendInput`/INSERT 增加 `ScheduleInstanceID`；`CompleteOutgoingSend` 等无需改动语义。
  - 确认实例发送（message_id NULL、draft_id 空）在 Sent upsert、`cleanupDeliveredDraft`、retention 清理、`outgoingSentClearSQL` 路径行为正确，必要时补最小适配（不得改动既有非实例路径）。
  - `ListOutgoingSendSummariesForUser` 增列 schedule_instance_id（供 UI 关联）。
- **Acceptance Criteria Addressed**: AC-4, AC-12, AC-16
- **Test Requirements**:
  - `rule` TR-6.1: 同账户可插入多条 message_id NULL 的实例发送且互不冲突；claim/complete/sent-copy 流程在存储测试中走通；证据扩展 `outgoing_test.go`。
  - `rule` TR-6.2: 既有非计划发送字段与行为零变化（既有测试通过）。

## Task 7: handler 层计划服务与 API
- **Status**: `pending`
- **Priority**: high
- **Depends On**: Task 3, Task 4, Task 6
- **Description**:
  - 新文件 `internal/handler/send_schedule.go`：
    - 表单解析/校验：`schedule_repeat`(none|daily|weekly)、`schedule_timezone`、`schedule_hour/minute`、`schedule_start_date`、`schedule_weekdays`、`schedule_end_date`；规则同 FR-3。
    - 扩展 `handleComposeSchedule`：none 走原路径；daily/weekly → saveComposeDraftFromForm → outgoingMessageFromDraft+sender → 复制附件到 NewScheduleVersion → CreateSendSchedule → DiscardIMAPDraft（计划先于删草稿提交，失败回滚并删除计划版本目录）→ publishMutation("scheduled")，响应含 schedule_id 与 next_occurrence_at。
    - `GET /api/send-schedules`（列表，含 next/状态/未终结计数）、`GET /api/send-schedules/{id}`（含设置与内容可编辑视图，附件为元数据列表）、`GET /api/send-schedules/{id}/instances`（分页结果）。
    - `POST /api/send-schedules/{id}` 更新设置；`POST /api/send-schedules/{id}/content` 用 compose 同构表单更新未来内容（附件整版本替换，旧版本目录在提交成功后删除）；`POST /api/send-schedules/{id}/pause|resume|end`（end 时若无未终结实例则尽力删除计划目录）。
    - builder 工厂：给定 cfg，把 contentJSON 恢复为 snapshot→生成新 MessageID/Date→`buildOutgoingMIME`→返回 PreparedScheduleSend（JMAP 账户在 JMAPEnabled=false 时返回带 MIME 的正常载荷，交由 claim 过滤）。
  - 路由同时注册到全局私有 mux（handler.go）与 `registerUserCompose`（用户路由）；CSRF/方法限制沿用同组端点。
- **Acceptance Criteria Addressed**: AC-1, AC-2, AC-3, AC-5, AC-15, AC-17
- **Test Requirements**:
  - `rule` TR-7.1: 表驱动校验测试（每类非法参数 400 且无计划/草稿残留）；合法 daily/weekly 创建后草稿已删除、计划内容与附件可读、原草稿删改不影响计划。
  - `rule` TR-7.2: 内容编辑替换附件后：未冻结下一次实例用新内容；已构造的实例快照字节不变。
  - `rule` TR-7.3: 归属测试：跨用户 GET/POST 全部 404。
  - `rule` TR-7.4: none 请求行为与单次预约逐字段一致（响应体、outgoing 行、无计划行）。

## Task 8: worker 物化接入（两条投递路径）与启动恢复
- **Status**: `pending`
- **Priority**: high
- **Depends On**: Task 5, Task 6, Task 7
- **Description**:
  - 全局 `StartOutgoingSendWorker`：启动恢复段与每次 tick/wake，在 `runDueOutgoingSends` 前查询有 active/paused 计划的账户，逐账户加载 cfg 后执行物化+对账；物化新产生的到期行随同一轮 claim 投递。
  - 路由路径：`NextAccountMailQueueAttempt` UNION `send_schedules.next_occurrence_at`（active 计划）；`userMailDelivery.Run` 在 send 循环前于账户租约内执行物化+对账（builder 用 s.cfg；与 send 同 5 轮次/冷却约束）。
  - 启动恢复：依赖既有 sending→ambiguous 恢复；增加一次全量/按账户 ReconcileScheduleInstances；物化器自身靠游标+唯一约束恢复。
- **Acceptance Criteria Addressed**: AC-9, AC-10, AC-12, AC-13, AC-14, AC-16
- **Test Requirements**:
  - `rule` TR-8.1: 到期计划在一次 worker tick 内完成 物化→claim→sent（SMTP 测试替身），Sent 中出现该 Message-ID；实例镜像 sent。
  - `rule` TR-8.2: 路由路径队列在 next_occurrence_at 到期时被唤醒（NextAccountMailQueueAttempt 测试）。
  - `rule` TR-8.3: 重启式测试（关闭重开 DB + 跑启动恢复）：sending 实例→ambiguous；计划游标不回退、无重复实例。
  - `rule` TR-8.4: 临时失败走既有退避；ambiguous 实例无 confirm 重试 409、带 confirm 成功，相邻日期实例独立。

## Task 9: 前端写信面板重复选项
- **Status**: `pending`
- **Priority**: high
- **Depends On**: Task 7
- **Description**:
  - `compose.templ` 的 `ComposeSchedulePopover`：新增重复方式 selectbox（Never/Daily/Weekly，name=compose_schedule_repeat）；weekly 显示周一至周日多选按钮组（name=compose_schedule_weekdays）；时区 selectbox（IANA 常用列表+浏览器当前区，name=compose_schedule_timezone，默认 getGoferTimezone）；可选结束日期 calendar（可清空）。pane 与主写信窗口两套 suffix 均适配。
  - `app.js`：`scheduleCompose` 追加 repeat/weekdays/timezone/end_date 字段与前端校验（weekly 至少一天；结束日期不早于开始）；none 时不发送循环字段；成功响应为计划时关闭窗口并刷新 Scheduled。
- **Acceptance Criteria Addressed**: AC-2, AC-17
- **Test Requirements**:
  - `rule` TR-9.1: templ 生成成功（`templ generate` 无 diff）；浏览器验证三种 repeat 下字段显隐与提交 payload；非法组合被前端拦截。
  - `rubric` TR-9.2: 面板可用性；scale 1-5；anchors 1=控件缺失/无法提交，3=可提交但显隐或默认值混乱，5=显隐/默认时区/校验/清空结束日期均自然；threshold >=4；证据浏览器走查截图。

## Task 10: 前端 Scheduled 计划管理视图
- **Status**: `pending`
- **Priority**: high
- **Depends On**: Task 8, Task 9
- **Description**:
  - Scheduled 文件夹顶部新增计划区（templ 容器 + app.js 渲染）：计划卡片展示规则摘要、时区本地时刻、下次发送（本地格式化）、状态、未终结数；操作：暂停/恢复/结束（confirm）、编辑设置（弹层：时刻/时区/星期/结束日期）、编辑未来内容（打开预填 compose，隐藏「发送/稍后」改「保存计划内容」，附件列示可删）。
  - 计划详情 dialog：各次实例表（本地日期、时刻、状态/原因、错误、尝试数、时间），失败/ambiguous 行提供与现 outgoing 一致的 retry/retry-now/cancel/确认重发操作（复用 `/api/outgoing-sends/{id}` 端点）。
  - 通过 SSE mutation("scheduled") 与现有轮询刷新计划区与实例状态；刷新页面后状态一致。
- **Acceptance Criteria Addressed**: AC-17, AC-14, AC-12
- **Test Requirements**:
  - `rule` TR-10.1: 浏览器端到端：创建 weekly 计划→卡片显示下次发送→（改时钟/短期计划到期）出现结果行→编辑未来内容→暂停显示 paused 且错过日期标 skipped→恢复→结束；全程无页面报错。
  - `rubric` TR-10.2: 管理闭环可用性；scale 1-5；anchors 1=只能看不能管，3=操作齐全但需多次跳转/状态不实时，5=单页闭环、状态实时、危险操作有确认；threshold >=4；证据浏览器走查记录。

## Task 11: Scheduled 角标与列表整合
- **Status**: `pending`
- **Priority**: medium
- **Depends On**: Task 10
- **Description**:
  - `GetFolderEmailCountFilteredForUser("scheduled")` 与侧栏 scheduledCount 增加 active/paused 且 `next_occurrence_at IS NOT NULL` 的计划计数（单次预约草稿计数保持不变）。
  - 计划变更后 publishMutation("scheduled") 驱动角标刷新；Scheduled 空态文案区分「无预约/无计划」。
- **Acceptance Criteria Addressed**: AC-17, AC-16
- **Test Requirements**:
  - `rule` TR-11.1: 存储计数测试：单次预约 + 2 个有效计划 + 1 个 ended 计划时角标=3；计划结束后减少。
  - `rule` TR-11.2: 现有 Scheduled 列表（单次预约草稿）展示与排序不回归。

## Task 12: 回归验证、构建与整体走查
- **Status**: `pending`
- **Priority**: high
- **Depends On**: Task 1 - Task 11
- **Description**:
  - `templ generate`、`go build ./...`、`go vet ./...`、`go test ./internal/...`；重点跑 handler/storage/mail/store/recurrence 包。
  - 核对 AC-16 清单：即时发送、单次预约、identity SMTP、Gmail/Outlook/JMAP、Sent 副本 ambiguous 恢复、文件清理。
  - 浏览器走查 AC-17 全流程并留存证据。
- **Acceptance Criteria Addressed**: AC-16, AC-17, AC-18
- **Test Requirements**:
  - `rule` TR-12.1: 全部命令退出码 0（或记录与本变更无关的既有失败并甄别）；证据完整输出。
  - `rubric` TR-12.2: 测试与实现质量总评；scale 1-5；anchors 1=核心边界无自动化测试，3=主路径覆盖但 DST/停机/崩溃缺一，5=全部边界覆盖且分层清晰；threshold >=4；证据测试清单与评审结论。
