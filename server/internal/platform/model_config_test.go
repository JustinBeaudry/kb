package platform

import (
	"fmt"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadModelConfig(t *testing.T) {
	valid := `[{"name":"primary","model":"fixture-model","max_output_tokens":2000,"input_micro_usd_per_mtok":4000000,"output_micro_usd_per_mtok":20000000}]`
	for _, tc := range []struct {
		name string
		env  map[string]string
		err  bool
		want func(*testing.T, ModelConfig)
	}{
		{"absent configuration leaves dreaming unconfigured", map[string]string{}, false, func(t *testing.T, c ModelConfig) {
			assert.Empty(t, c.Routes)
			assert.Zero(t, c.DailyCapMicroUSD)
		}},
		{"owner configuration", map[string]string{"KB_MODEL_ROUTES": valid, "KB_MODEL_DAILY_CAP_MICRO_USD": "5000000", "KB_DIGEST_HOUR_UTC": "9", "KB_MODEL_API_KEY": "fixture"}, false, func(t *testing.T, c ModelConfig) {
			require.Len(t, c.Routes, 1)
			assert.True(t, c.Routes[0].Priced())
			assert.Equal(t, int64(5_000_000), c.DailyCapMicroUSD)
			assert.Equal(t, 9, c.DigestHourUTC)
			assert.NotContains(t, fmt.Sprintf("%v %#v", c, c), "fixture")
		}},
		{"unpriced route is accepted but not priced", map[string]string{"KB_MODEL_ROUTES": `[{"name":"p","model":"m","max_output_tokens":10}]`}, false, func(t *testing.T, c ModelConfig) {
			assert.False(t, c.Routes[0].Priced())
		}},
		{"malformed routes", map[string]string{"KB_MODEL_ROUTES": `{`}, true, nil},
		{"duplicate route", map[string]string{"KB_MODEL_ROUTES": `[{"name":"p","model":"m","max_output_tokens":10},{"name":"p","model":"m","max_output_tokens":10}]`}, true, nil},
		{"unbounded output", map[string]string{"KB_MODEL_ROUTES": `[{"name":"p","model":"m","max_output_tokens":10000000}]`}, true, nil},
		{"negative cap", map[string]string{"KB_MODEL_DAILY_CAP_MICRO_USD": "-1"}, true, nil},
		{"invalid hour", map[string]string{"KB_DIGEST_HOUR_UTC": "24"}, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range []string{"KB_MODEL_ROUTES", "KB_MODEL_DAILY_CAP_MICRO_USD", "KB_DIGEST_HOUR_UTC", "KB_MODEL_API_KEY", "KB_MODEL_ENDPOINT"} {
				t.Setenv(k, tc.env[k])
			}

			c, err := LoadModelConfig()

			if tc.err {
				assert.ErrorIs(t, err, domain.ErrInvalidInput)
				return
			}
			require.NoError(t, err)
			tc.want(t, c)
		})
	}
}
