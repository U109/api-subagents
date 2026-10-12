package config

import (
	"errors"
	"strings"
	"unicode"
)

// CatalogModel 保存已读取的候选目录和原生容量，不作为模型选择或接入范围的授权依据。
type CatalogModel struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Context int    `json:"context,omitempty"`
}

// normalizeCatalog 校验无凭据的候选目录并保留顺序，最多 2000 个候选；选择范围仍仅由 Model/RelayModels 决定。
func normalizeCatalog(catalog []CatalogModel) ([]CatalogModel, error) {
	if len(catalog) > 2000 {
		return nil, errors.New("候选模型目录最多 2000 项。")
	}
	result := make([]CatalogModel, 0, len(catalog))
	seen := map[string]bool{}
	for _, model := range catalog {
		model.ID = strings.TrimSpace(model.ID)
		if model.ID == "" || len([]rune(model.ID)) > 200 || strings.IndexFunc(model.ID, unicode.IsControl) >= 0 || len([]rune(model.Name)) > 200 || strings.IndexFunc(model.Name, unicode.IsControl) >= 0 || model.Context < 0 || model.Context > 100000000 {
			return nil, errors.New("候选模型的标识、名称或容量无效。")
		}
		if seen[model.ID] {
			return nil, errors.New("候选模型目录存在重复标识。")
		}
		seen[model.ID] = true
		result = append(result, model)
	}
	return result, nil
}
