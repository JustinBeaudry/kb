package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLifecycleIdentityDigestPreservesOpaqueTupleBoundaries(t *testing.T) {
	values := [][]string{{"project", "task"}, {"pro", "jecttask"}, {"project/task", "🧪"}, {"project", strings.Repeat("t", 8192) + "/🧪"}, {"project", "a", "b", "c"}}
	seen := map[string]bool{}
	for _, parts := range values {
		key := LifecycleIdentityDigest(parts...)
		assert.Len(t, key, 64)
		assert.Equal(t, key, LifecycleIdentityDigest(parts...))
		assert.False(t, seen[key])
		seen[key] = true
	}
}
