package platform

import (
	"encoding/json"
	"os"
	"strconv"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// ModelConfig is owner configuration for background dreaming. Every field is
// optional: absent routes, pricing, key or cap leave dreaming jobs pending
// with a visible reason instead of inventing spend permission.
type ModelConfig struct {
	Endpoint, APIKey string
	Routes           []domain.ModelRoute
	DailyCapMicroUSD int64
	DigestHourUTC    int
}

func (ModelConfig) String() string   { return "platform.ModelConfig([redacted])" }
func (ModelConfig) GoString() string { return "platform.ModelConfig([redacted])" }

type routeConfig struct {
	Name                  string `json:"name"`
	Model                 string `json:"model"`
	MaxOutputTokens       int64  `json:"max_output_tokens"`
	InputMicroUSDPerMTok  int64  `json:"input_micro_usd_per_mtok"`
	OutputMicroUSDPerMTok int64  `json:"output_micro_usd_per_mtok"`
}

// LoadModelConfig reads KB_MODEL_* variables. Malformed values fail closed.
func LoadModelConfig() (ModelConfig, error) {
	c := ModelConfig{Endpoint: os.Getenv("KB_MODEL_ENDPOINT"), APIKey: os.Getenv("KB_MODEL_API_KEY")}
	if raw := os.Getenv("KB_MODEL_ROUTES"); raw != "" {
		var routes []routeConfig
		if json.Unmarshal([]byte(raw), &routes) != nil || len(routes) > 10 {
			return ModelConfig{}, domain.ErrInvalidInput
		}
		seen := map[string]bool{}
		for _, r := range routes {
			route := domain.ModelRoute{Name: r.Name, Model: r.Model, MaxOutputTokens: r.MaxOutputTokens, InputMicroUSDPerMTok: r.InputMicroUSDPerMTok, OutputMicroUSDPerMTok: r.OutputMicroUSDPerMTok}
			if !route.Valid() || seen[route.Name] {
				return ModelConfig{}, domain.ErrInvalidInput
			}
			seen[route.Name] = true
			c.Routes = append(c.Routes, route)
		}
	}
	var err error
	if c.DailyCapMicroUSD, err = optionalInt("KB_MODEL_DAILY_CAP_MICRO_USD", 0, domain.MaxPriceMicroUSDPerMTok); err != nil {
		return ModelConfig{}, err
	}
	hour, err := optionalInt("KB_DIGEST_HOUR_UTC", 0, 23)
	if err != nil {
		return ModelConfig{}, err
	}
	c.DigestHourUTC = int(hour)
	return c, nil
}

func optionalInt(name string, minimum, maximum int64) (int64, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < minimum || v > maximum {
		return 0, domain.ErrInvalidInput
	}
	return v, nil
}
