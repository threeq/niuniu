# 数据源 Owner 配置（团队版可用）— 设计

日期：2026-09-09
状态：已确认（用户批准）

## 背景

数据源行本身有 owner（owner_type/owner_id，per-owner UNIQUE(name)），但：

- 设置页创建端点把归属硬编码为调用者个人，没有选 org 的入口；
- 项目关联三端点把 orgIDs 写死为 nil —— org 名下的源在关联面板对所有人
  报「不存在」；个人源绑到 org 项目后也只有创建者自己的 agent 能看到
  （ownerVisible 对其他成员为 false）。

结果：团队版下设置路径的数据源基本不可用。MCP/agent 路径（继承工作区
owner）没有此问题（已真实环境实测）。

## 设计（已批准）

### 后端（api/data_source.go；service 层零改动）

1. 创建时可选归属：POST /me/data-sources body 新增 owner_type/owner_id
   （可选，缺省 = 个人，向后兼容）：
   - 个人 owner：必须等于调用者；
   - org owner：EnsureOwnerWritable 校验成员身份（org 任意成员可创建，
     与平台现有资源语义一致）。
2. caller 的 orgIDs（Authz.Accessible(uid).OrgIDs，带缓存）贯通全部设置侧
   端点：List / Get / Update / Delete / Verify / ListProjectSources /
   AddProjectSource / RemoveProjectSource。
3. agent 代理路径不动（wsOrgIDs(ws) 本来就对）。

### 前端

4. 创建对话框加「归属」选择：个人 / 所属组织（个人版只有「个人」，
   行为不变）；列表项加归属徽标。
5. i18n 三语言（zh-CN / zh-TW / en）。

### 测试

- api 层：org 成员创建 org 源；成员 B 列表可见；org 项目关联面板可见
  可绑；跨个人 owner 仍 404；不传 owner 时行为与旧版一致。
- docker 真实链路补一条 org 归属（org 源 + org 工作区 agent 可见）。

### 明确不做

- 存量个人源的「转移为 org」操作；
- org 源的细粒度成员权限（写对全员开放，与「任意成员可创建」对称）。
