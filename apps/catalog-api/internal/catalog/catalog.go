package catalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed manifest.json
var manifest []byte

type Item struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	DurationSeconds int    `json:"durationSeconds"`
	Available       bool   `json:"available"`
	MediaType       string `json:"mediaType,omitempty"`
	CoverURL        string `json:"coverUrl,omitempty"`
}

type Catalog struct {
	Items []Item `json:"items"`
}

func Load() (Catalog, error) {
	return decode(manifest)
}

func decode(source []byte) (Catalog, error) {
	var result Catalog
	if err := json.Unmarshal(source, &result); err != nil {
		return Catalog{}, fmt.Errorf("decode embedded catalog: %w", err)
	}
	return result, nil
}
