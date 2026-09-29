package main

// Quotes and the paid-call ledger (design §7.4 hard guardrails).
//
//	quotes/<quote_id>.json       报价单：分项 + 总额，quote_estimate 产出，是 L3
//	                             确认（ask_user）的前置输入。
//	quotes/<quote_id>.calls.json 调用留痕：image_generate / video_generate 在
//	                             付费调用【前】写入 submitted 记录，调用结束后更新为
//	                             succeeded / failed（含供应商错误原文）。崩溃/中断
//	                             也会留下 submitted 痕迹，费用可回溯。
//
// Money is 元 (CNY). Prices come from, in order: the caller's explicit
// unit_price, the adapter's registered price metadata (overridable through
// NN_CAP_<CAP>_PRICE). A price that is known nowhere stays 0 and the quote
// marks the item `price_configured=false` with a warning — the module never
// invents a price.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"sync"
	"time"
)

// QuoteItem is one line of a quote.
type QuoteItem struct {
	Capability      string  `json:"capability"` // tts | image | video
	Backend         string  `json:"backend"`
	Model           string  `json:"model,omitempty"`
	Label           string  `json:"label,omitempty"` // 自由标注（如 "第3镜 首帧"）
	Quantity        float64 `json:"quantity"`        // tts=字符数 · image=张数 · video=秒数
	Unit            string  `json:"unit"`
	UnitPrice       float64 `json:"unit_price"`
	Amount          float64 `json:"amount"`
	PriceConfigured bool    `json:"price_configured"`
	Note            string  `json:"note,omitempty"`
}

// Quote is the persisted quote document.
type Quote struct {
	QuoteID     string      `json:"quote_id"`
	CreatedAt   string      `json:"created_at"`
	Currency    string      `json:"currency"`
	QualityTier string      `json:"quality_tier,omitempty"` // 草稿/标准/精制（预算-质量交易）
	Note        string      `json:"note,omitempty"`
	Items       []QuoteItem `json:"items"`
	Total       float64     `json:"total"`
	Warnings    []string    `json:"warnings,omitempty"`
}

// CallRecord is one paid call, written before the call and finalized after.
type CallRecord struct {
	CallID     string  `json:"call_id"`
	Capability string  `json:"capability"`
	Backend    string  `json:"backend"`
	Model      string  `json:"model,omitempty"`
	Label      string  `json:"label,omitempty"`
	Quantity   float64 `json:"quantity"`
	Unit       string  `json:"unit,omitempty"`
	UnitPrice  float64 `json:"unit_price"`
	Amount     float64 `json:"amount"`
	Status     string  `json:"status"` // submitted | succeeded | failed
	TaskID     string  `json:"task_id,omitempty"`
	Files      string  `json:"files,omitempty"`
	Error      string  `json:"error,omitempty"`
	StartedAt  string  `json:"started_at"`
	FinishedAt string  `json:"finished_at,omitempty"`
}

// CallLedger is the per-quote call trace document.
type CallLedger struct {
	QuoteID string       `json:"quote_id"`
	Calls   []CallRecord `json:"calls"`
}

// ledgerMu serializes read-modify-write of the ledger files (MCP tool calls can
// arrive concurrently from the client).
var ledgerMu sync.Mutex

// newID mints "q-20260927-153045-1a2b" style ids (sortable + unique).
func newID(prefix string) string {
	buf := make([]byte, 2)
	if _, err := rand.Read(buf); err != nil {
		copy(buf, []byte{0xde, 0xad})
	}
	return fmt.Sprintf("%s-%s-%s", prefix, time.Now().Format("20060102-150405"), hex.EncodeToString(buf))
}

func newQuoteID() string { return newID("q") }
func newCallID() string  { return newID("c") }

func roundMoney(f float64) float64 { return math.Round(f*10000) / 10000 }

// quoteRequestItem is the tool-facing item shape (before price resolution).
type quoteRequestItem struct {
	Capability string
	Backend    string
	Model      string
	Label      string
	Quantity   float64
	UnitPrice  *float64
	Unit       string
}

// buildQuote resolves prices for every item and assembles the quote document.
// Capability-level configuration problems fail the whole quote (there is
// nothing sensible to quote against a missing account); a missing *price* only
// degrades the item to price_configured=false + warning.
func buildQuote(reg *Registry, items []quoteRequestItem, qualityTier, note string) (*Quote, error) {
	if len(items) == 0 {
		return nil, fmt.Errorf("报价清单为空：quote_estimate 需要至少一个 items 条目")
	}
	q := &Quote{
		QuoteID:     newQuoteID(),
		CreatedAt:   time.Now().Format(time.RFC3339),
		Currency:    "CNY",
		QualityTier: qualityTier,
		Note:        note,
	}
	for i, it := range items {
		idx := fmt.Sprintf("items[%d]", i)
		if it.Quantity <= 0 {
			return nil, fmt.Errorf("%s：quantity 必须为正数（tts=字符数 · image=张数 · video=秒数）", idx)
		}
		var (
			bindingName string
			model       string
			meta        PriceMeta
		)
		switch it.Capability {
		case CapTTS:
			if reg.TTS == nil {
				return nil, fmt.Errorf("%s（tts）：%s", idx, reg.UnconfiguredMessage(CapTTS, "配音"))
			}
			bindingName, model, meta = reg.TTS.Name, reg.TTS.Model, reg.TTS.Price
		case CapImage:
			if reg.Image == nil {
				return nil, fmt.Errorf("%s（image）：%s", idx, reg.UnconfiguredMessage(CapImage, "图像"))
			}
			bindingName, model, meta = reg.Image.Name, reg.Image.Model, reg.Image.Price
		case CapVideo:
			if reg.Video == nil {
				return nil, fmt.Errorf("%s（video）：%s", idx, reg.UnconfiguredMessage(CapVideo, "视频"))
			}
			bindingName, model, meta = reg.Video.Name, reg.Video.Model, reg.Video.Price
		default:
			return nil, fmt.Errorf("%s：capability %q 不合法（必须是 tts / image / video）", idx, it.Capability)
		}
		if it.Backend != "" && it.Backend != bindingName {
			q.Warnings = append(q.Warnings, fmt.Sprintf("%s：清单声明的后端 %q 与当前配置的 %q 不一致，价格按清单/元数据取值。",
				idx, it.Backend, bindingName))
			bindingName = it.Backend
			meta.UnitPrice = 0
			meta.Configured = false
			meta.Note = "该后端不是当前配置的后端，请显式给出 unit_price。"
		}
		if it.Model != "" {
			model = it.Model
		}

		// Resolve the unit price: caller-supplied wins over adapter metadata.
		effective := meta
		note := ""
		if it.UnitPrice != nil {
			effective.UnitPrice = *it.UnitPrice
			effective.Configured = true
		}
		if it.Unit != "" {
			effective.Unit = it.Unit
		}
		if !effective.Configured {
			note = effective.Note
			if note == "" {
				note = "单价未配置，金额按 0 计。"
			}
			q.Warnings = append(q.Warnings, fmt.Sprintf("%s（%s/%s）：%s", idx, it.Capability, bindingName, note))
		}
		item := QuoteItem{
			Capability:      it.Capability,
			Backend:         bindingName,
			Model:           model,
			Label:           it.Label,
			Quantity:        it.Quantity,
			Unit:            effective.Unit,
			UnitPrice:       effective.UnitPrice,
			Amount:          roundMoney(effective.amount(it.Quantity)),
			PriceConfigured: effective.Configured,
			Note:            note,
		}
		q.Items = append(q.Items, item)
		q.Total = roundMoney(q.Total + item.Amount)
	}
	if len(q.Warnings) > 0 {
		q.Warnings = append(q.Warnings, "提示：单价为 0 的条目不会产生真实费用；L3（视频）在派发前必须经用户确认。")
	}
	return q, nil
}

// quoteIDRe is the exact shape minted by newQuoteID: q-YYYYMMDD-HHMMSS-<4 hex>.
var quoteIDRe = regexp.MustCompile(`^q-[0-9]{8}-[0-9]{6}-[0-9a-f]{4}$`)

// validQuoteID reports whether id has the minted shape. Quote ids become file
// names (quotes/<id>.json), so an unvalidated id is a path-traversal hole —
// keep this check in front of every id→path conversion.
func validQuoteID(id string) bool { return quoteIDRe.MatchString(id) }

// invalidQuoteIDError renders the actionable rejection for a malformed id.
func invalidQuoteIDError(quoteID string) error {
	return fmt.Errorf("quote_id %q 格式不合法：应为 quote_estimate 返回的 id（形如 q-20260927-153045-1a2b），不可包含路径字符", quoteID)
}

// quotePath returns quotes/<quote_id>.json, rejecting any id that was not
// minted by quote_estimate.
func quotePath(wsDir, quoteID string) (string, error) {
	if !validQuoteID(quoteID) {
		return "", invalidQuoteIDError(quoteID)
	}
	return projectPath(wsDir, "quotes", quoteID+".json"), nil
}

// ledgerPath returns quotes/<quote_id>.calls.json (same id discipline).
func ledgerPath(wsDir, quoteID string) (string, error) {
	if !validQuoteID(quoteID) {
		return "", invalidQuoteIDError(quoteID)
	}
	return projectPath(wsDir, "quotes", quoteID+".calls.json"), nil
}

// writeQuote persists the quote and returns its workspace-relative path.
func writeQuote(wsDir string, q *Quote) (string, error) {
	if err := ensureDirs(wsDir); err != nil {
		return "", err
	}
	path, err := quotePath(wsDir, q.QuoteID)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(q, "", "  ")
	if err != nil {
		return "", fmt.Errorf("序列化报价单失败: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("写入报价单 %s 失败: %w", path, err)
	}
	return relToWS(wsDir, path), nil
}

// requireQuote verifies the quote exists (paid calls must reference a real
// quote produced by quote_estimate).
func requireQuote(wsDir, quoteID string) error {
	if quoteID == "" {
		return fmt.Errorf("缺少 quote_id：付费调用前必须先调用 quote_estimate 生成报价单并把 quote_id 传进来（L2 留痕 / L3 需经用户确认）")
	}
	path, err := quotePath(wsDir, quoteID)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("报价单 %s 不存在（%s）：请先调用 quote_estimate 生成报价单", quoteID, relToWS(wsDir, path))
	}
	return nil
}

// appendCall writes a call record (status submitted for pre-call traces) into
// the quote's ledger, creating the ledger when needed.
func appendCall(wsDir, quoteID string, rec CallRecord) error {
	if quoteID == "" {
		return nil
	}
	if rec.CallID == "" {
		rec.CallID = newCallID()
	}
	if rec.StartedAt == "" {
		rec.StartedAt = time.Now().Format(time.RFC3339)
	}
	ledgerMu.Lock()
	defer ledgerMu.Unlock()
	ledger, err := loadLedger(wsDir, quoteID)
	if err != nil {
		return err
	}
	ledger.Calls = append(ledger.Calls, rec)
	return saveLedger(wsDir, ledger)
}

// finishCall finalizes the most recent open call matching callID.
func finishCall(wsDir, quoteID, callID string, mutate func(*CallRecord)) error {
	if quoteID == "" || callID == "" {
		return nil
	}
	ledgerMu.Lock()
	defer ledgerMu.Unlock()
	ledger, err := loadLedger(wsDir, quoteID)
	if err != nil {
		return err
	}
	found := false
	for i := range ledger.Calls {
		if ledger.Calls[i].CallID == callID {
			mutate(&ledger.Calls[i])
			if ledger.Calls[i].FinishedAt == "" {
				ledger.Calls[i].FinishedAt = time.Now().Format(time.RFC3339)
			}
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("调用留痕 %s 未找到（报价单 %s）", callID, quoteID)
	}
	return saveLedger(wsDir, ledger)
}

func loadLedger(wsDir, quoteID string) (*CallLedger, error) {
	path, err := ledgerPath(wsDir, quoteID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &CallLedger{QuoteID: quoteID}, nil
		}
		return nil, fmt.Errorf("读取调用留痕 %s 失败: %w", path, err)
	}
	var ledger CallLedger
	if err := json.Unmarshal(data, &ledger); err != nil {
		return nil, fmt.Errorf("解析调用留痕 %s 失败: %w", path, err)
	}
	ledger.QuoteID = quoteID
	return &ledger, nil
}

func saveLedger(wsDir string, ledger *CallLedger) error {
	if err := ensureDirs(wsDir); err != nil {
		return err
	}
	data, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化调用留痕失败: %w", err)
	}
	path, err := ledgerPath(wsDir, ledger.QuoteID)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("写入调用留痕 %s 失败: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("提交调用留痕 %s 失败: %w", path, err)
	}
	return nil
}

// amountFor prices a call using the binding's metadata (or the item's explicit
// price when the caller passed one through the quote).
func amountFor(meta PriceMeta, quantity float64) float64 { return roundMoney(meta.amount(quantity)) }
