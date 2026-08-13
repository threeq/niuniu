# 仓库创建自动检测输入类型

**日期**: 2026-08-13
**状态**: 设计中

## 背景

当前仓库创建对话框有两个需要手动切换的 tab：本地路径 / 远程地址。用户需要先选择正确的 tab 再输入，增加了操作步骤。本设计将两个 tab 合并为单一输入框，根据输入内容自动判断是本地路径还是远程地址。

## 设计

### 前端检测规则（纯前端，无后端改动）

优先级从高到低：

1. **URL scheme 匹配** → 远程地址
   - `https://...`, `http://...`
   - `git@host:path` (SCP-style)
   - `git://...`, `ssh://...`, `ftp://...`, `ftps://...`, `file://...`
2. **Windows 盘符** → 本地路径
   - `C:\...`, `D:\...` 等（`/^[a-zA-Z]:[\\/]/`）
3. **Unix 绝对路径** → 本地路径
   - `/home/user/...`, `/Users/...`
4. **波浪号** → 本地路径
   - `~/Projects/...`
5. **空输入或无法判断** → 默认本地

### UI 变化

- 移除 `mode` 切换 tab（两个按钮）
- 单一输入框，placeholder 统一提示
- 右侧保留浏览按钮（DirectoryBrowser），点击选中目录后自动填入
- 输入框下方根据检测结果动态显示：
  - **远程时**：名称输入（自动提取）、提示文字
  - **本地时**：名称输入（自动提取）、提示文字、autoInit checkbox

### 不改动的部分

- 后端 API 不变（`POST /api/repositories` 同时接受 `path` 和 `remote_url`）
- `DirectoryBrowser` 组件不变
- 提交逻辑不变（`autoDetectType` 结果决定发 `path` 还是 `remote_url`）

### 涉及文件

| 文件 | 改动 |
|------|------|
| `server/web/src/components/dialogs/create-repository-dialog.tsx` | 主要重构：移除 tab，添加自动检测，合并输入 |
| `server/web/src/i18n/locales/zh-CN/repositories.json` | 新增/修改翻译 key |
| `server/web/src/i18n/locales/en/repositories.json` | 英文翻译同步 |

### 检测函数

```typescript
function detectRepoType(input: string): 'local' | 'remote' {
  const trimmed = input.trim();
  if (!trimmed) return 'local';
  // URL schemes
  if (/^(https?|git|ssh|ftp|ftps|file):\/\//i.test(trimmed)) return 'remote';
  // SCP-style: git@host:path
  if (/^git@/.test(trimmed)) return 'remote';
  // Windows drive letter
  if (/^[a-zA-Z]:[\\/]/.test(trimmed)) return 'local';
  // Unix absolute path
  if (/^\//.test(trimmed)) return 'local';
  // Tilde
  if (/^~/.test(trimmed)) return 'local';
  // Default
  return 'local';
}
```