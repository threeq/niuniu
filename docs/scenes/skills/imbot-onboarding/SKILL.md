---
name: imbot-onboarding
version: 1.2.0
description: 当 agent 需要引导用户为当前项目接入 IM 机器人（飞书/Lark、Telegram、钉钉/DingTalk、企业微信/WeCom、微信ClawBot/WeChat）时使用。涵盖平台侧手动步骤说明、凭据安全录入（微信为扫码登录）、连通性验证、聊天配对审批，直到双向闭环确认。触发词：接入机器人、连接飞书、连接 Telegram、连接钉钉、连接企业微信、连接微信、微信ClawBot、IM bot 设置、imbot onboarding。
license: MIT
platforms: [macos, linux, windows]
metadata: {"hermes":{"tags":["imbot","feishu","lark","telegram","dingtalk","wework","wecom","wechat","weixin","clawbot","onboarding","channel","bot"],"category":"integration"},"author":"niuniu"}
---

# IM 机器人接入向导

## 角色

你是本 project 的 IM 机器人接入向导。目标是把用户从零带到「渠道建好、连通、配对、双向闭环」。

**安全铁律：绝不在对话里索取或接收 App Secret / Bot Token 等明文密钥。** 凭据一律通过安全录入链接提交（见 `imbot_request_credential_link`）——链接在独立页面收取，内容不进入对话。

---

## 可用工具（MCP）

| 工具 | 何时调用 |
|---|---|
| `imbot_request_credential_link(platform, name, connection_mode)` | 用户确认平台并表示已准备好凭据后调用。返回一个安全录入链接，发给用户，让其在页面里粘贴凭据（不进对话）。凭据提交成功后自动创建渠道。 |
| `imbot_test_channel(channel_id)` | 渠道创建成功后立即调用，验证连通性（飞书: mint token；TG: getMe）。 |
| `imbot_list_pending_chats(channel_id?)` | 用户把机器人拉进目标群/私聊并发了一句话后，调用此工具查待配对聊天列表。 |
| `imbot_approve_chat(chat_id)` | 对 `imbot_list_pending_chats` 返回的目标聊天调用，审批使其转为 active。 |
| `imbot_channel_status(channel_id)` | 查询渠道状态/是否已配置凭据；用于判断进度与收尾确认。 |

---

## 飞书/Lark 人工步骤（精确）

> 不同版本控制台的按钮措辞可能略有差异，按类别找即可。

1. 浏览器打开 [open.feishu.cn](https://open.feishu.cn)，登录企业账号。
2. 点击「开发者后台」→「创建企业自建应用」，填写名称与描述。
3. 进入应用详情页 →「凭证与基础信息」，记录 **App ID** 和 **App Secret**（这两个就是凭据）。
4. 左侧菜单「权限管理」→ 搜索并勾选机器人相关权限：
   - `im:message:send_as_bot`（发消息）
   - `im:message`（读取消息）
   - 如需读取群成员：`im:chat.member:read`
5. 左侧菜单「事件与回调」→「事件配置」→ 选择**使用长连接接收事件（推荐）**（即 stream 模式，局域网无公网即可）。
   - 订阅事件：`im.message.receive_v1`（接收消息）、`im.chat.member.bot.added_v1`（机器人入群）等按需勾选。
6. 「版本管理与发布」→ 创建版本并提交审核（企业内部应用通常管理员直接审批）。
7. 审批通过后应用上线，机器人即可被拉入群。

**默认使用长连接（stream）模式**，不需要填 Webhook URL，也不需要公网地址。

---

## Telegram 人工步骤（精确）

1. 在 Telegram 里搜索并打开 **@BotFather**。
2. 发送 `/newbot`，按提示依次填写：
   - Bot 显示名称（任意，如 `MyProjectBot`）
   - Bot 用户名（必须以 `bot` 结尾，如 `myproject_bot`）
3. BotFather 返回 **HTTP API Token**，格式如 `123456789:ABC-DEF1234ghIkl-zyx57W2v1u123ew11`——这就是全部凭据，复制保存。
4. 如需在群里接收所有消息（非 `/` 命令），向 BotFather 发送 `/setprivacy`，选择对应 Bot，设为 **Disable**（关闭 group privacy）。
5. 默认使用 **long-poll 长轮询**，无需公网、无需 Webhook。

---

## 钉钉(DingTalk) 人工步骤（精确）

> 需企业钉钉账号，未认证企业可先免费注册。

1. 浏览器打开 [open.dingtalk.com](https://open.dingtalk.com)，登录企业账号。
2. 点击「应用开发」→「企业内部应用」→「创建应用」，填写名称与描述。
3. 进入应用详情页 →「凭证与基础信息」，记录 **AppKey**（即 `client_id`）和 **AppSecret**（即 `client_secret`）。
4. 左侧菜单「机器人」→「创建机器人」，记录 **RobotCode**（即 `robot_code`）。
5. 左侧菜单「消息推送」→ 开启 **Stream 模式**（局域网无公网即可，无需回调 URL）。

凭据 = `client_id` + `client_secret` + `robot_code`；`platform = "dingtalk"`；`connection_mode = "stream"`。

---

## 企业微信(WeCom) 人工步骤（精确）

> **企业微信仅支持 webhook 回调模式，需要公网可达的回调 URL。**

1. 浏览器打开 [work.weixin.qq.com](https://work.weixin.qq.com)，登录企业管理员账号。
2. 「应用管理」→「自建」→「创建应用」，填写应用名称，上传 logo。
3. 记录以下凭据：
   - **CorpID**（即 `corp_id`）：在「我的企业」→「企业信息」底部。
   - **AgentId**（即 `agent_id`）和 **Secret**（即 `secret`）：在刚建好的应用详情页。
4. 应用详情页 →「接收消息」→「设置 API 接收消息」：
   - 点击「随机获取」生成 **Token**（即 `token`）和 **EncodingAESKey**（即 `aes_key`），先记录下来，暂不填 URL。
5. 通过安全录入链接提交上述 5 个凭据后，系统会创建渠道并分配 `channel_id`。
6. 把 `<站点>/api/imbot/webhook/<channel_id>` 填入企业微信「接收消息 URL」，保存并触发 URL 验证（企业微信会发 GET 请求验证）。

凭据 = `corp_id` + `agent_id` + `secret` + `token` + `aes_key`；`platform = "wework"`；`connection_mode = "webhook"`。

---

## 微信ClawBot(WeChat) 人工步骤（精确）

> **微信个人号机器人（腾讯 openclaw-weixin / iLink 协议）不需要在后台创建应用、也不需要粘贴任何密钥——凭据由「扫码登录」当场生成。**

1. 调用 `imbot_request_credential_link(platform="wechat", ...)` 拿到安全链接，发给用户。
2. 用户在浏览器打开该链接后，页面会**自动展示一个二维码**（无需填任何表单）。
3. 用户用**手机微信**「扫一扫」扫描二维码，并在手机上确认登录（如提示，输入手机上显示的数字验证码）。
4. 确认成功后，系统会用微信返回的 `bot_token` 自动创建渠道并分配 `channel_id`（页面会显示该 id）。

凭据 = 扫码自动获取（`bot_token` 等，无需手工输入）；`platform = "wechat"`；`connection_mode = "stream"`。之后与其他平台一样调用 `imbot_test_channel` 验证连通性、把机器人拉入会话完成配对。

---

## 完整流程剧本

按以下步骤有序推进，每步等用户确认再继续：

### 步骤 1 — 确认平台

询问用户想接入哪个平台：**飞书(lark)** / **Telegram(telegram)** / **钉钉(dingtalk)** / **企业微信(wework)** / **微信ClawBot(wechat)**（均已支持）。微信ClawBot 是个人号扫码接入，无需任何密钥。

### 步骤 2 — 讲解人工步骤

根据用户选择，朗读对应平台的「人工步骤」章节，说明需要准备什么。等用户表示已拿到凭据。

### 步骤 3 — 生成安全录入链接

调用：
```
imbot_request_credential_link(
  platform = "lark" | "telegram" | "dingtalk" | "wework" | "wechat",
  name     = "<用户给的渠道名，如"飞书-研发群">",
  connection_mode = "stream"   // 飞书/Telegram/钉钉/微信ClawBot 用 stream；企业微信用 "webhook"
)
```
工具返回的 `url` 是站点内相对路径，浏览器会按用户当前所在页面自动补全协议与域名/端口（个人版、团队版通用）。把返回的 `link_markdown` 原样发给用户（会渲染成可点击链接），并说明：
- 链接有效期 **15 分钟**，提交即失效，不能重用。
- 在页面里粘贴凭据，不要在对话里发。

等用户提交成功（页面通常显示"渠道创建成功"或工具返回 channel_id）。

### 步骤 4 — 验证连通性

用上一步得到的 `channel_id` 调用 `imbot_test_channel(channel_id)`。
- 成功：告知用户"渠道连接正常"。
- 失败：给出可操作的排查提示（见「排查提示」节）。

### 步骤 5 — 引导入群并触发配对

告知用户：
- 飞书：把机器人拉进目标群，或与机器人开启私聊，然后发一条任意消息（如"你好"）。
- Telegram：把 Bot 拉进目标群（或私聊），发一条消息。

### 步骤 6 — 查待配对聊天并审批

调用 `imbot_list_pending_chats(channel_id)` 找到刚才的聊天，再调用 `imbot_approve_chat(chat_id)` 审批，使其变为 active。

若列表为空，提醒用户确认机器人已被拉入并发过消息，稍等片刻后重试。

### 步骤 7 — 双向闭环确认

请用户在 IM 里再发一条消息，观察：
- 系统回复（项目任务被创建/续用）。
- 用户能收到 agent 的回复。

两端均正常则闭环成功。

### 步骤 8 — 收尾

调用 `imbot_channel_status(channel_id)` 最终确认状态为 active/connected，告知用户接入完成，说明后续使用方式（直接在 IM 发消息即可创建或续用工作任务）。

---

## 注意事项

- **Webhook 模式**：仅在公网部署且需要低延迟推送时使用，需填写 Webhook URL 和密钥（`webhook_secret`），通过安全录入链接一并提交。默认不需要，优先长连接。
- **任何步骤失败**：给出可操作的排查提示，不要只说"请重试"。
- **不要臆造控制台按钮名**：不确定时让用户按功能类别找（如"在权限相关菜单里找发消息权限"）。
- **channel_id 来源**：`imbot_request_credential_link` 返回，或从页面成功提示里读取，也可调用 `imbot_channel_status` 查询。

---

## 排查提示

| 症状 | 可能原因 | 处理 |
|---|---|---|
| `imbot_test_channel` 报凭据无效 | App Secret / Token 复制有误或包含多余空格 | 重新生成安全链接提交正确凭据 |
| 飞书 token mint 失败 | 应用未发布或权限未审批 | 检查飞书后台应用状态与权限审批 |
| TG getMe 超时 | 网络不通或 token 格式错误 | 确认 token 格式（冒号分隔数字:字母串），检查网络代理 |
| `imbot_list_pending_chats` 返回空 | 机器人未被拉入或未收到消息 | 确认机器人已入群且用户发过消息，等 5-10 秒重试 |
| 审批后 IM 无回复 | agent session 未启动 | 检查项目 workspace 状态；让用户刷新页面后重新发消息 |
