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
