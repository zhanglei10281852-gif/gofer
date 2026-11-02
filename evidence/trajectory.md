# 在发送记录中关联逐收件人的 SMTP 投递报告

> **Workspace:** F:\swe\098872\project-05

---

**User**

一封信发给几个人后，发件人可能陆续收到延迟或退信通知，而发送记录里的成功只说明服务器接收了邮件。请加入默认关闭的 SMTP 投递状态通知选项：用户选择后，支持 DSN 的 SMTP 服务器收到与这次发送绑定的信封标识及逐收件人通知请求；服务器不支持时仍按普通邮件发送，并明确显示无法请求报告，未选择的发送和非 SMTP 发送保持原行为。选择要随草稿保存及预约发送保留，普通发送重试沿用原请求身份；SMTP 接受结果和后来收到的报告分别显示，没有报告不能被解释成已经投递或失败。用户通过现有读信入口打开同一账号收到的标准 multipart/report 投递报告时，应依据报告中的信封标识和收件人信息关联此前实际请求报告的发送，分别展示有效收件人的延迟、投递成功或失败；不匹配的标识、未在原信封中的地址、无法解释的段落保持为普通邮件内容，不能改写其他发送，报告中某个无效段落不应妨碍其他有效收件人的结果。发送完成清除 MIME 和临时快照后，在现有发送记录保留期内仍能关联报告，包括密送收件人，但密送地址不能因此进入发给其他人的邮件正文或头部。再次打开、重复收到报告或服务重启不能重复累计同一报告，晚到的延迟通知不能把已确认的成功或失败退回等待；正文下载失败时原结果不变，报告更新提交失败后，再次打开已缓存正文也能补做未确认的处理，不能重新发送原信来恢复。发送详情须如实展示各收件人的不同结果，原报告仍能阅读和下载，报告关联随对应发送记录结束保留而清理，原有发送不确定提示、人工重试确认和 Sent 副本处理不受影响。

**Assistant**

我先来了解一下项目结构和现有代码，再规划实现方案。

toolName: view_folder

status: success

f:\swe\098872\project-05

这是一个 Go 语言的邮件客户端项目。这个需求涉及 SMTP DSN（投递状态通知，RFC 3461/3464）端到端功能。我先深入了解现有的发信、SMTP 客户端、邮件读取与记录保留相关代码。

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\mail\smtp\client.go

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing.go

需求涉及面很广。我继续探查存储层、MIME 构建、写信入口、读信入口和保留期逻辑。

toolName: search_by_regex

status: success

toolName: search_by_regex

status: failed

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing.go

我查看 go-smtp 库版本对 DSN 的支持情况，以及 MIME 构建、写信/读信相关代码。

toolName: Shell

status: success

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

go-smtp v0.25.0 客户端已支持 DSN。我查看其客户端 API 细节。

toolName: view_files

status: success

filePath: C:\Users\czxy4\go\pkg\mod\github.com\emersion\go-smtp@v0.25.0\client.go

go-smtp 客户端 DSN API 已明确（`MailOptions.EnvelopeID/Return`、`RcptOptions.Notify`、`Extension("DSN")`）。现在我需要全面梳理写信、读信、发送记录、保留期等链路。让我并行读取关键文件。

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing_status.go

toolName: search_by_regex

status: success

这个功能端到端链路较长。我先建立任务清单，然后继续摸清剩余环节。

toolName: todo_write

status: success

Todos updated: 8 items

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\handler\user_compose.go

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\handler\handler.go

继续探查视图层、草稿存储结构、读信入口与保留期清理。

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: view_folder

status: success

f:\swe\098872\project-05\internal\views

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\storage\retention.go

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\handler\user_mail_body.go

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\handler\user_mail_views.go

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

功能链路很长，我继续查看前端写信/发送状态逻辑、compose 模板、草稿存储与 JMAP 读信路径。

toolName: search_by_regex

status: success

toolName: Shell

status: success

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\handler\jmap_mail_body.go

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\mail\message\parse.go

继续查看 messages 表结构、compose 模板和邮件原始 MIME 的本地缓存路径。

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\storage\schema.sql

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\storage\storage.go

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\storage\storage.go

现在查看写信模板，找到发送选项区域。

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\views\compose.templ

查看 app.js 中写信表单提交、草稿填充与重置逻辑。

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\assets\js\app.js

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\assets\js\app.js

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\assets\js\app.js

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\assets\js\app.js

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\assets\js\app.js

查看发送状态在前端的详情展示方式（点击状态标记后的面板等）。

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\assets\js\app.js

查看邮件阅读视图模板与打开邮件的处理逻辑，确定投递结果的展示位置。

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\handler\handler.go

toolName: search_by_regex

status: success

查看邮件视图模板结构，确定投递结果面板的挂载位置。

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\views\mailview.templ

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\handler\handler.go

我已基本掌握全貌。现在查看剩余关键细节：OutgoingMessage 结构、Email 模型、草稿保存 SQL、SMTP 调用方、路由注册与 templ 工具。

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\mail\message\write.go

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\handler\user_storage.go

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\mail\smtp\client_test.go

toolName: search_by_regex

status: success

查看前端邮件打开/iframe 加载的挂钩点。

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\assets\js\app.js

查看发送后本地 Sent 消息记录的保存逻辑，确认 Sent 消息锚点。

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing_test.go

调研完成，开始实现。先查看最新迁移函数的写法以保持风格一致。

toolName: todo_write

status: success

Todos updated: 9 items

toolName: search_by_regex

status: success

开始实现。首先创建 DSN 存储层与数据库迁移。

toolName: Write

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing_dsn.go

changes: undefined

文件中 `DeliveryStatusForMessage` 逻辑未写完，且 Sent 锚点应按 Internet Message-ID 关联。我来修正这几处。

toolName: edit_file_search_replace

status: failed

filePath: f:\swe\098872\project-05\internal\storage\outgoing_dsn.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing_dsn.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing_dsn.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing_dsn.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing_dsn.go

changes: undefined

现在重写 `DeliveryStatusForMessage`，正确处理报告消息与 Sent 消息两种锚点：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing_dsn.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing_dsn.go

changes: undefined

现在更新 `outgoing.go` 存储层：队列表结构、SQL、扫描与摘要。

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing.go

changes: undefined

现在更新 SELECT 列表与扫描函数：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing.go

changes: undefined

注册迁移：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\storage.go

changes: undefined

先编译 storage 包验证目前的改动：

toolName: Shell

status: success

存储层完成。现在创建 DSN（RFC 3461/3464）解析包。

toolName: todo_write

status: success

Todos updated: 9 items

toolName: Write

status: success

filePath: f:\swe\098872\project-05\internal\mail\dsn\report.go

changes: undefined

修正解析器：按字段而非位置区分消息块/收件人块，并修复空 Action 的 panic：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\mail\dsn\report.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\mail\dsn\report.go

changes: undefined

toolName: Shell

status: success

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\mail\dsn\report.go

changes: undefined

toolName: Shell

status: success

解析包编译通过。现在写解析器测试：

toolName: Write

status: success

filePath: f:\swe\098872\project-05\internal\mail\dsn\report_test.go

changes: undefined

toolName: Shell

status: success

解析器测试通过。现在为 SMTP 客户端增加 DSN 支持。先确认库的选项字段名：

toolName: search_by_regex

status: success

现在修改 SMTP 客户端加入 DSN 能力协商与参数发送：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\mail\smtp\client.go

changes: undefined

现在更新包级发送函数，新增带 DSN 的投递入口：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\mail\smtp\client.go

changes: undefined

toolName: Shell

status: success

现在写 SMTP 客户端 DSN 测试（用原生 TCP 假服务器）：

toolName: Write

status: success

filePath: f:\swe\098872\project-05\internal\mail\smtp\dsn_test.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\mail\smtp\dsn_test.go

changes: undefined

toolName: Shell

status: success

toolName: Shell

status: success

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\mail\smtp\client_test.go

这个测试的计时在改动前后路径相同，可能是 Windows 时钟精度导致的偶发。重试验证：

toolName: Shell

status: success

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\mail\smtp\client.go

代码看起来没问题。用 git worktree 在原始代码上验证该测试是否本来就通过：

toolName: Shell

status: success

toolName: Shell

status: success

toolName: Shell

status: success

计时测试在原始代码上同样失败（此 Windows 环境时钟精度问题），与本次改动无关。继续后端集成。先给 OutgoingMessage 加字段：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\mail\message\write.go

changes: undefined

现在集成到发信流程。更新快照结构与入队逻辑：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing.go

changes: undefined

现在改造 SMTP 投递函数返回 DSN 协商结果：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing.go

changes: undefined

现在在投递主流程中捕获 DSN 结果并记录：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing.go

changes: undefined

成功路径上也要记录接受结果：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing.go

changes: undefined

更新 `PrepareOutgoingSend` 以同步 DSN 请求标志（保留 ENVID）：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing.go

changes: undefined

`PrepareOutgoingSend` 还要能在首次开启时写入 ENVID：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing.go

changes: undefined

更新两个调用点：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing.go

changes: undefined

现在更新路由模式下待发送草稿的编辑输入：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\user_compose.go

changes: undefined

toolName: Shell

status: success

编译通过。现在接入表单字段与草稿选项。先改 `handleCompose`：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\handler.go

changes: undefined

现在创建处理器侧的 DSN 关联服务与状态 API：

toolName: Write

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing_dsn.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing_dsn.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing_dsn.go

changes: undefined

现在把关联处理挂到两个读信正文入口：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\user_mail_body.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\handler.go

changes: undefined

toolName: search_by_regex

status: success

注册新路由（两种部署模式）：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\handler.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\user_storage.go

changes: undefined

现在把 DSN 选项随草稿保存/读取。先处理单用户模式：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\handler.go

changes: undefined

路由模式的草稿保存：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\user_compose.go

changes: undefined

预约发送从草稿重建消息时读取选项：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing.go

changes: undefined

草稿读取 API 返回 DSN 选项：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\handler.go

changes: undefined

现在扩展发送状态 API，返回 DSN 状态与逐收件人结果：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing_status.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing_status.go

changes: undefined

在三个状态接口中填充收件人结果：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing_status.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing_status.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing_status.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing_status.go

changes: undefined

toolName: Shell

status: success

全项目编译通过。运行相关包的测试验证 SQL 与行为：

toolName: Shell

status: success

现在编写存储层 DSN 状态机测试：

toolName: Write

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing_dsn_test.go

changes: undefined

toolName: Shell

status: success

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing_dsn_test.go

changes: undefined

toolName: Shell

status: success

测试库初始化停在 133。查看测试 DB 初始化逻辑：

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\storage\storage.go

toolName: search_by_regex

status: success

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\storage.go

changes: undefined

toolName: search_by_regex

status: success

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\schema.sql

changes: undefined

toolName: Shell

status: success

修复几个问题：重复报告副本的关联键冲突、测试夹具。先修存储层关联插入：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing_dsn.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing_dsn.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing_dsn.go

changes: undefined

修复测试夹具：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing_dsn_test.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing_dsn_test.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing_dsn_test.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing_dsn_test.go

changes: undefined

toolName: Shell

status: success

存储层测试通过。运行整个 storage 包回归：

toolName: Shell

status: success

这些失败看起来是 Windows 路径 URI 问题。检查是否与本次改动相关：

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\storage\user_storage_migration_stage_test.go

为确认这些失败是环境既有问题，在原始代码上运行其中一个失败测试：

toolName: Shell

status: success

toolName: Shell

status: success

确认是环境既有失败，与改动无关。恢复改动：

toolName: Shell

status: success

检查恢复后的文件状态：

toolName: Shell

status: success

toolName: Shell

status: success

文件状态正常。重新编译并跑核心测试：

toolName: Shell

status: success

toolName: Shell

status: success

迁移测试使用精简数据库，需在 V134 迁移中加存在性保护：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing_dsn.go

changes: undefined

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing_dsn.go

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing_dsn.go

changes: undefined

toolName: Shell

status: success

剩余失败是同类 Windows URI 环境问题。运行 handler 包回归测试：

toolName: Shell

status: success

趁测试在后台运行，开始前端工作。先在写信模板中加入 DSN 开关：

toolName: edit_file_search_replace

status: failed

filePath: f:\swe\098872\project-05\internal\views\compose.templ

changes: undefined

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\views\compose.templ

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\views\compose.templ

changes: undefined

Pane 页脚同样加入：

toolName: edit_file_search_replace

status: failed

filePath: f:\swe\098872\project-05\internal\views\compose.templ

changes: undefined

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\views\compose.templ

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\views\compose.templ

changes: undefined

添加 `ComposeDSNToggle` 组件（含移动端菜单项）：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\views\compose.templ

changes: undefined

移动端更多菜单中也加入该选项：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\views\compose.templ

changes: undefined

在邮件视图中加入投递结果面板挂载点：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\views\mailview.templ

changes: undefined

运行 templ 生成代码：

toolName: Shell

status: success

toolName: Shell

status: success

templ 重新生成了全部文件，检查差异性质，避免无关改动：

toolName: Shell

status: success

差异仅为换行符。确认我真正需要的两个生成文件有实质改动，并还原其余无关文件：

toolName: Shell

status: success

toolName: Shell

status: success

templ 输出 LF 而仓库为 CRLF，导致全文件假改动。将所有生成文件转回 CRLF：

toolName: Shell

status: success

toolName: Shell

status: success

其余文件仅是行尾差异且 `git diff` 无实质内容，恢复它们，只保留真正改动的两个生成文件：

toolName: Shell

status: success

toolName: Shell

status: success

toolName: Shell

status: success

toolName: Shell

status: success

toolName: Shell

status: success

单个恢复有效，重新批量恢复并刷新索引：

toolName: Shell

status: success

toolName: Shell

status: success

toolName: Shell

status: success

toolName: Shell

status: success

toolName: Shell

status: success

toolName: Shell

status: success

toolName: Shell

status: success

改用直接按 HEAD 内容写出 CRLF 的方式恢复无关生成文件：

toolName: Shell

status: success

toolName: Shell

status: success

现在只剩两个目标文件。将它们转为 CRLF 以保持最小差异：

toolName: Shell

status: success

生成的 Go 内容与提交版本差异很大，查看具体差异性质：

toolName: Shell

status: success

差异是 templ 插入代码后的行号/索引级联更新，属正常。编译并运行 views 包测试：

toolName: Shell

status: success

检查 handler 测试后台任务的失败内容：

toolName: Shell

status: success

toolName: Shell

status: success

日志中没有 DSN 相关错误。针对性运行发信/写信相关测试：

toolName: Shell

status: success

失败仍是同一个 Windows URI 环境问题。运行不依赖用户存储迁移的核心发信测试：

toolName: Shell

status: success

toolName: Shell

status: success

核心发信测试通过，其余失败均为同一 Windows URI 环境问题。现在接线 app.js：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\assets\js\app.js

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\assets\js\app.js

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\assets\js\app.js

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\assets\js\app.js

changes: undefined

现在添加投递状态面板的前端获取与渲染：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\assets\js\app.js

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\assets\js\app.js

changes: undefined

在发送状态徽章的 tooltip 中加入 DSN 摘要：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\assets\js\app.js

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\assets\js\app.js

changes: undefined

校验 app.js 语法：

toolName: Shell

status: success

发送后草稿自动清理的快照对比需要包含 DSN 选项：

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing.go

检查现有 handler 测试夹具，评估端到端关联测试：

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing_test.go

toolName: search_by_regex

status: success

写处理器层端到端关联测试：

toolName: Write

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing_dsn_test.go

changes: undefined

toolName: Shell

status: success

toolName: Shell

status: success

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing_dsn_test.go

changes: undefined

toolName: Shell

status: success

toolName: search_by_regex

status: success

让 `publishMutation` 对 nil 依赖安全（关联处理是尽力而为）：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\handler.go

changes: undefined

toolName: Shell

status: success

全部通过。运行 gofmt，并检查生产环境是否需要压缩版 JS：

toolName: Shell

status: success

toolName: search_by_regex

status: success

确认路由模式下 SMTP 发信走哪条 worker 路径：

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\handler\user_mail_delivery.go

关键发现：路由模式有独立的 SMTP 投递路径，需要同样接入 DSN。查看 `finishSMTP` 全貌：

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\handler\user_mail_delivery.go

路由模式的 SMTP 路径也需要接入 DSN。修改 `send`、`sendIdentitySMTP` 与 `finishSMTP`：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\user_mail_delivery.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\user_mail_delivery.go

changes: undefined

现在更新 `finishSMTP` 签名并在成功/不确定时记录接受结果：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\user_mail_delivery.go

changes: undefined

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\handler\user_mail_delivery.go

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\user_mail_delivery.go

changes: undefined

toolName: Shell

status: success

toolName: search_by_regex

status: success

toolName: Shell

status: success

toolName: Shell

status: success

仍是 Windows URI 环境问题。运行不涉及存储迁移暂存的路由投递相关测试：

toolName: Shell

status: success

toolName: Shell

status: success

修正一个边界：重试到不支持 DSN 的服务器时，不能删除早前已协商信封的关联数据：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing_dsn.go

changes: undefined

toolName: Shell

status: success

检查面板所用的 emerald 样式类是否存在于预构建 CSS 中：

toolName: Shell

status: success

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

清理存储测试中的无用导入：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing_dsn_test.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing_dsn_test.go

changes: undefined

toolName: Shell

status: success

补充测试：入队时 DSN 选项与传输方式的关系：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing_dsn_test.go

changes: undefined

重写该测试，去除无意义且会违反外键的部分：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing_dsn_test.go

changes: undefined

修正测试：入队冲突需要 message_id，插入消息行并补导入：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing_dsn_test.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing_dsn_test.go

changes: undefined

`queueOutgoingMessage` 对关联草稿有校验，简化该测试（保留逻辑已在存储层测试覆盖）：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing_dsn_test.go

changes: undefined

toolName: Shell

status: success

toolName: search_by_regex

status: success

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing_dsn_test.go

changes: undefined

toolName: Shell

status: success

toolName: Shell

status: success

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing_dsn_test.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098872\project-05\internal\handler\outgoing_dsn_test.go

changes: undefined

toolName: Shell

status: success

运行完整 vet/build，并确认生成代码包含新标记：

toolName: Shell

status: success

全部通过。运行 mail 子系统全部测试做回归：

toolName: Shell

status: success

toolName: Shell

status: success

toolName: Shell

status: success

确认全部为环境既有失败。最后总览改动清单：

toolName: Shell

status: success

toolName: Shell

status: success

toolName: Shell

status: success

发现两个意外改动的测试文件（可能是 gofmt 触及），检查差异：

toolName: Shell

status: success

无实质差异，恢复这两个文件：

toolName: Shell

status: success

干净了。通读最终存储实现以排查逻辑问题：

toolName: view_files

status: success

filePath: f:\swe\098872\project-05\internal\storage\outgoing_dsn.go

检查发送状态相关测试是否对响应 JSON 做精确断言：

toolName: Shell

status: success

确认 Pane 表单也生成了开关：

toolName: Shell

status: success

运行保留期剪枝测试，确认新外键不影响清理：

toolName: Shell

status: success

最终验证：全量构建、vet 与全部 DSN 相关测试：

toolName: Shell

status: success

所有验证通过。更新任务状态：

toolName: todo_write

status: success

Todos updated: 9 items

已完成 SMTP 投递状态通知（DSN，RFC 3461/3464）端到端支持。核心实现如下：

## 行为设计

**写信选项（默认关闭）**
- 写信对话框与全屏写信页新增 "Delivery report" 勾选框（含移动端菜单），不勾选时一切行为不变。
- 选项随草稿保存（私有表 `draft_send_options`，绝不进入邮件头/正文），预约发送时保留；立即发送、重试、改发件身份都沿用同一条发送记录。
- 仅 SMTP 发送生效；Gmail/Outlook/JMAP 以及日历回复完全不受影响。

**SMTP 投递（[client.go](file:///f:/swe/098872/project-05/internal/mail/smtp/client.go)）**
- 入队时生成一次 `ENVID`（`g`+32 位十六进制），重试/草稿编辑永不更换。
- 先探测服务器是否通告 `DSN`：支持则 `MAIL FROM … ENVID=… RET=HDRS`、每个 `RCPT TO … NOTIFY=SUCCESS,FAILURE,DELAY`（ASCII 地址附带 ORCPT）；不支持则普通发送，并明确记录 `dsn_state='unsupported'`，界面显示"无法请求报告"而不是猜测投递结果。
- DATA 阶段不确定（ambiguous）时也记录协商结果，报告仍可化解歧义。

**报告关联（读信即处理）**
- 新解析包 [report.go](file:///f:/swe/098872/project-05/internal/mail/dsn/report.go)：识别标准 `multipart/report; report-type=delivery-status`，解析 Original-Envelope-Id（xtext）、Action/Status/Diagnostic，按 Message-ID（缺失时内容哈希）去重；非 DSN/MDN/缺机器部分的邮件保持原样。
- 在两种读信正文入口（[user_mail_body.go](file:///f:/swe/098872/project-05/internal/handler/user_mail_body.go)、[handler.go](file:///f:/swe/098872/project-05/internal/handler/handler.go)）打开时处理：**同账号 + ENVID 匹配 + 收件人在原信封内**才入账；坏段落/陌生地址/不匹配标识不影响其他收件人，也不改写任何发送。
- 状态机单调：delivered/failed 终态不可逆，迟到的 delayed 不会回退；重复打开、重复投递副本、重启都不重复累计。提交失败不留标记，再次打开缓存正文可补做；全程不重发原信。

**展示与隐私**
- 服务器接受与报告分离：等待中明确显示 "Waiting for report"，绝不等于已投递。
- 邮件视图顶部面板逐收件人显示 延迟/成功/失败+SMTP 诊断码；Sent 消息和入站报告均可查看；原报告邮件照常阅读、查看源码、下载。
- 密送仅存在于私有信封表（含 MIME/快照清理后 30 天保留期内可关联），SMTP 邮件本就不含 Bcc；状态接口仅发送者本人可见。
- 关联数据通过外键级联随发送记录保留期清理（[outgoing_dsn.go](file:///f:/swe/098872/project-05/internal/storage/outgoing_dsn.go)，迁移 V133→V134）。

## 测试
- 新增：DSN 解析器、SMTP 命令协商（原生 TCP 假服务器，含不支持场景）、存储状态机（幂等/单调/去重/密送/级联/锚点查询）、处理器端到端关联测试，全部通过。
- 既有发信、重试、Sent 副本、写信草稿、发送状态接口等回归测试通过。
- 说明：本机一批既有用例（`invalid uri authority: C:` 的用户存储迁移暂存、Windows 时钟精度的计时断言）在未改动的原始代码上同样失败，属本 Windows 环境问题，与本次改动无关。
- 前端新增的 emerald 等样式类已存在于其他模板中，执行 `task generate` 时会一并编入 output.css。
