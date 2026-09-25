# Attribution

## RSI 探索式预热（`internal/rsi`，`niuniu-agent explore`）

方法思路借鉴自 **RSIAgent**（AetherLabsAI，Apache-2.0，arXiv:2609.15364）：
Curriculum（自生成练习任务）→ Actor（沙箱执行）→ Verifier（独立验证）
三角色递归自我改进，广-深两阶段探索，以及「仅验证通过的尝试沉淀经验」。

本项目为 **idea 级借鉴 + 独立 clean-room 实现**（Go，零第三方依赖），
未复制其任何代码或提示词。与原工作的关键差异：

- Verifier 为纯规则判定（复用 P6 eval 的 RunChecks），不做模型 judge；
- 信息防火墙是结构性的：Verifier 只接触任务 checks 与沙箱目录，无法
  读取 Actor 的推理轨迹或记忆；
- 经验沉淀带接地约束：失败尝试零沉淀（直接对冲原工作自述的「记忆保留
  错误规则」失效模式）；
- 效果门槛：RSI 开/关用 P6 eval 完成率对比衡量，增益不显著不默认启用。

## 适应度门控自进化 / think-first 提案（`cmd evolve`、`internal/rsi`）

方法思路借鉴自 **OpenRSI**（AlexWortega/OpenRsi，未附 LICENSE 文件）：
公开/私有评测分离（迭代用公开集、采纳看私有集）、think-first 提案先行
（因果机制 + 预期增益 + 证伪条件，缺一不评审）、对抗复验（胜者换新求解
重测均值仍胜才采纳）、分体裁路由（任务分类 → 同体裁经验优先召回）。

本项目为 **idea 级借鉴 + 独立 clean-room 实现**（Go，零第三方依赖），
未复制其任何代码或提示词；因原仓库无 LICENSE，仅注明方法来源，
不构成代码衍生。关键差异：

- 私有集判定零容差（新通过率 ≥ 旧通过率才采纳，相等也采纳）；
- think-first 提案缺任一段直接拒绝，不做模型 judge；
- 与 RSIAgent 借鉴的接地约束组合：失败尝试零沉淀，双闸防「错误规则入记忆」。
