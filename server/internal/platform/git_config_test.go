package platform

import (
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadGitRepositories(t *testing.T) {
	cases := []struct {
		name, raw string
		want      map[string]string
		err       error
	}{
		{"unset is empty", "", map[string]string{}, nil},
		{"single mapping", "demo=/srv/repos/demo", map[string]string{"demo": "/srv/repos/demo"}, nil},
		{"several mappings", "a=/r/a,b=/r/b", map[string]string{"a": "/r/a", "b": "/r/b"}, nil},
		{"missing separator", "demo", nil, domain.ErrInvalidInput},
		{"empty id", "=/r/a", nil, domain.ErrInvalidInput},
		{"empty path", "demo=", nil, domain.ErrInvalidInput},
		{"duplicate id", "a=/r/a,a=/r/b", nil, domain.ErrInvalidInput},
		{"empty entry", "a=/r/a,,b=/r/b", nil, domain.ErrInvalidInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KB_GIT_REPOSITORIES", tc.raw)

			got, err := LoadGitRepositories()

			if tc.err != nil {
				assert.ErrorIs(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
