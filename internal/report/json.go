package report

import (
	"encoding/json"

	"github.com/eterny13/mig-est/internal/model"
)

// JSON renders the stable machine-readable schema.
func JSON(r model.Report) (string, error) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}
