package usage

import (
	_ "embed"
	"encoding/json"
	"errors"
	"math"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"
)

const OfficialPriceDate = "2026-10-14"

//go:embed official-prices.json
var officialPricesJSON []byte

var officialPrices = loadOfficialPrices()

// loadOfficialPrices 读取随程序发布的公开价格表；任何不完整或非法项都阻止启动而非默认为零价。
func loadOfficialPrices() map[string]Price {
	var prices map[string]Price
	if err := json.Unmarshal(officialPricesJSON, &prices); err != nil {
		panic("内置官方费率格式无效")
	}
	for _, price := range prices {
		if validatePrice(price) != nil {
			panic("内置官方费率数值无效")
		}
	}
	return prices
}

// OfficialPrice 只匹配准确模型 ID，返回独立副本；私有别名、未知型号和 fast 后缀均不猜测价格。
func OfficialPrice(model string) (Price, bool) {
	price, ok := officialPrices[model]
	if !ok {
		return Price{}, false
	}
	var result Price
	_ = json.Unmarshal(shared.Marshal(price), &result)
	return result, true
}

// EffectivePrices 为已配置且已选模型补官方默认，保留显式覆盖及不可见历史项；不写真实配置。
func (s *Store) EffectivePrices(c configstore.Config) (Prices, error) {
	prices, err := s.Prices()
	if err != nil {
		return prices, err
	}
	for name, profile := range c.Models {
		id := profile.ID
		if id == "" {
			id = name
		}
		for _, model := range profile.OrderedModels() {
			key := Key(id, model)
			if _, exists := prices.Rates[key]; exists {
				continue
			}
			if price, ok := OfficialPrice(model); ok {
				prices.Rates[key] = price
			}
		}
	}
	return prices, nil
}

// HandleConfig 沿用费率修订和账本查询边界，只为返回结果补充当前配置的官方价格。
func (s *Store) HandleConfig(route string, body []byte, c configstore.Config) (shared.Object, error) {
	result, err := s.Handle(route, body)
	if err != nil {
		return result, err
	}
	prices, err := s.EffectivePrices(c)
	if err != nil {
		return nil, err
	}
	result["prices"] = prices
	return result, nil
}

// completeRate 拒绝缺失、null 或非数字的四项价格；显式零合法，不能用缺项替代零。
func completeRate(raw json.RawMessage) bool {
	var fields map[string]*float64
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	for _, field := range []string{"input", "output", "read", "write"} {
		if fields[field] == nil {
			return false
		}
	}
	return true
}

// validatePrice 校验普通和 Fast 的有限数值及上下文边界，避免通过嵌套费率绕过旧校验。
func validatePrice(price Price) error {
	rates := []Rate{{price.Input, price.Output, price.Read, price.Write}}
	if price.Fast != nil {
		rates = append(rates, *price.Fast)
	}
	if price.LongContext != nil {
		if price.LongContext.Above <= 0 || price.LongContext.Above > 10000000 {
			return errors.New("长上下文价格阈值无效")
		}
		rates = append(rates, price.LongContext.Rate)
		if price.LongContext.Fast != nil {
			rates = append(rates, *price.LongContext.Fast)
		}
	}
	if len(price.Source) > 512 || len(price.CheckedAt) > 32 || len(price.Note) > 512 {
		return errors.New("费率来源说明过长")
	}
	for _, rate := range rates {
		for _, value := range []float64{rate.Input, rate.Output, rate.Read, rate.Write} {
			if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 10000 {
				return errors.New("单价须为 0–10000 的有限数值")
			}
		}
	}
	return nil
}

// settlementPrice 按实际速度及输入规模选择完整快照；缺失 Fast 价不沿用普通价，保持未定价。
func settlementPrice(price Price, fast bool, tokens Tokens) (Price, bool) {
	rate := Rate{price.Input, price.Output, price.Read, price.Write}
	fastRate := price.Fast
	if price.LongContext != nil {
		if tokens.Input == nil {
			return Price{}, false
		}
		if *tokens.Input > price.LongContext.Above {
			rate, fastRate = price.LongContext.Rate, price.LongContext.Fast
		}
	}
	if fast {
		if fastRate == nil {
			return Price{}, false
		}
		rate = *fastRate
	}
	return Price{Input: rate.Input, Output: rate.Output, Read: rate.Read, Write: rate.Write}, true
}
