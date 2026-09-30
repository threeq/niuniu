---
name: jumpserver-bastion
version: 1.0.0
description: 当 agent 需要通过 JumpServer 堡垒机（跳板机）操作远程服务器时使用。涵盖：盘点授权资产（REST API）、SSH 直连资产（jms用户@资产账号@资产IP 格式，端口 2222）、接入 JumpServer 官方 MCP Server、堡垒机安全纪律与常见故障排查。触发词：jumpserver、堡垒机、跳板机、bastion、jms、资产清单、连服务器、运维跳板。
license: MIT
platforms: [macos, linux, windows]
metadata: {"hermes":{"tags":["jumpserver","bastion","堡垒机","跳板机","ssh","ops","运维"],"category":"integration"},"author":"niuniu"}
---

# JumpServer 堡垒机操作向导

## 角色

你在帮用户通过 JumpServer 堡垒机操作其管理的远程服务器（资产）。典型诉求：
「看看我有哪些机器」「连上 xx 机器查个东西」「在服务器上执行/部署 xx」。

**安全铁律：**
1. **所有操作必须经堡垒机进行**——JumpServer 的价值在于审计与授权，绝不建议用户
   绕开堡垒机直连资产（拿到资产真实口令也不行）。
2. **凭据只在环境变量里**。从 `JUMPSERVER_API_TOKEN` 等环境变量读取，绝不在对话、
   命令输出、日志或写入的文件里回显 token/口令明文；用户在对话里粘贴了明文凭据时，
   提醒其改为在工作空间环境变量里配置，并不要复述该值。
3. **破坏性操作先确认**：重启/删除/改配置/清理磁盘等动作，先复述将做什么、影响哪台
   资产，等用户确认再执行；无人值守模式下禁止执行这类操作，只产出计划。
4. 只操作**已授权**的资产：API 拿不到的资产就是没授权，不要尝试猜 IP / 爆破 / 越权。

---

## 前置信息（环境变量）

| 环境变量 | 必填 | 说明 |
|---|---|---|
| `JUMPSERVER_URL` | 是 | 堡垒机地址，如 `https://jms.example.com`（内网常见 `http://`） |
| `JUMPSERVER_API_TOKEN` | 调 API 时 | Personal Access Token，Web 控制台右上角「个人设置 → API Token」创建 |
| `JUMPSERVER_SSH_PORT` | 否 | Koko SSH 端口，默认 `2222` |
| `JUMPSERVER_SSH_USER` | 否 | 堡垒机登录用户名（直连资产时要用） |

用户没配时，指导其在 niuniu 工作空间的「环境变量」设置里添加，**不要让用户把 token
粘贴进对话**。先探测：

```bash
echo "JUMPSERVER_URL=${JUMPSERVER_URL:-未配置} TOKEN_SET=$([ -n "$JUMPSERVER_API_TOKEN" ] && echo yes || echo no)"
```

---

## 能力 A：盘点授权资产（REST API）

当前用户被授权的资产列表（推荐入口，普通用户即可调用）：

```bash
curl -sS ${JUMP_INSECURE:---insecure} "${JUMPSERVER_URL%/}/api/v1/perms/users/self/assets/?limit=100&offset=0" \
  -H "Authorization: Bearer $JUMPSERVER_API_TOKEN"
```

- 分页：`?limit=&offset=`；过滤：`&search=关键字`；返回字段含 `hostname`、`ip`、
  `id`、`platform`、`comment` 等，以实例为准。
- 管理员可用全量接口 `/api/v1/assets/assets/`；节点/分组见 `/api/v1/assets/nodes/`。
- **各版本端点有差异**：以实例自带 Swagger 为准（浏览器打开
  `${JUMPSERVER_URL}/api/docs/`，或 curl 拉 Swagger JSON 查端点）。
- 401/403 → token 过期或未授权：请用户重新生成 PAT，或确认该账号确有资产授权。
- 自签名证书报错：curl 加 `-k`（脚本里用 `--insecure`）即可，内网部署常见。

---

## 能力 B：连接资产（SSH 直连）

Koko 组件的 SSH 端口默认 **2222**。两种姿势：

```bash
# 1) 登录堡垒机，交互式选资产
ssh -p 2222 <jms用户>@<堡垒机地址>

# 2) 直连指定资产（省去选单）——注意是两层 @ 拼接
ssh -p 2222 <jms用户>@<资产账号>@<资产IP> <堡垒机地址>
# 例：jms 账号 alice、目标机器用 root、资产 10.0.0.8：
ssh -p 2222 alice@root@10.0.0.8 jms.example.com
```

要点与坑：

- **不支持 SSH ProxyJump / `-J`**（官方 issue #11392）：不能把 JumpServer 当
  `ProxyJump` 跳板去连第三台机器，也不能用它转发 scp/sftp 端口。文件传输走
  堡垒机的 Web 终端 / 网盘功能，或先落到自己有权限的中转机。
- 这是**交互式会话**（Koko 代理 + 命令审计）：非交互式地 `ssh ... "cmd"` 通常不可靠。
  agent 需要在资产上执行命令时，用 `-tt` 强制伪终端进入会话后逐条驱动，或让用户在
  Web 终端里操作；批量/自动化执行走 JumpServer「作业中心」，端点以 `/api/docs/` 为准。
- 认证失败时区分：堡垒机密码 / MFA 未过、该资产未授权给此用户、资产账号（第二段）
  写错。

---

## 能力 C：接入 JumpServer 官方 MCP Server（可选增强）

JumpServer v4.9+ 提供官方 MCP Server（[jumpserver/mcp](https://github.com/jumpserver/mcp)），
把资产查询/会话等暴露为 MCP 工具。用户想要更完整的 agent-堡垒机集成时指导部署：

```bash
# 1) 生成 Bearer token（或直接用能力 A 里的 PAT）
TOKEN=$(curl -s -X POST "${JUMPSERVER_URL%/}/api/v1/authentication/auth/" \
  -H "Content-Type: application/json" \
  -d '{"username":"<用户名>","password":"<密码>"}' | jq -r '.token')

# 2) 起容器（.env 里写 api_token=xxx 与 jumpserver_url=http://jms-host）
docker run -d -p 8099:8099 --env-file .env --name jms_mcp ghcr.io/jumpserver/mcp:latest
```

MCP 配置（在 niuniu 工作空间「MCP」设置里添加，type 为 SSE）：

```json
{
  "type": "sse",
  "url": "http://127.0.0.1:8099/sse",
  "headers": { "Authorization": "Bearer <token>" }
}
```

注意：token 写在 MCP 配置里属于敏感信息，建议用户自行操作这一步；agent 不代填 token。

---

## 故障排查速查

| 现象 | 先查 |
|---|---|
| API 401 | token 过期/删了；`Authorization: Bearer` 头写对没有 |
| API 403 | 该账号未被授权目标资产/节点，找管理员加授权 |
| SSH 连不上 2222 | Koko 端口被改过（`JUMPSERVER_SSH_PORT`）；防火墙/安全组 |
| SSH 后卡在选单 | 想直连就用三段式 `jms用户@资产账号@资产IP`，两层 @ 缺一不可 |
| 证书报错 | 内网自签名，curl 加 `-k`，或给实例换正规证书 |

---

## 沟通约定

- 面向用户用业务话术（「你的堡垒机」「这台机器」「授权资产」），少堆端点与参数。
- 汇报盘点结果用表格：主机名、IP、平台、备注；机器多时先给数量再按需展开。
- 用户提「连 xx 机器」时：先从盘点结果里确认目标资产与资产账号，再给直连命令；
  交互式会话里执行命令前说明「这些命令会经堡垒机审计」。
