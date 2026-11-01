package graph

import (
	"github.com/JustinBeaudry/kb/server/migrations"
	"github.com/stretchr/testify/assert"
	"strings"
	"testing"
)

func TestOwnerAdjudicationMigrationIsEmbedded(t *testing.T) {
	assert.True(t, strings.Contains(migrations.Verification, "owner_decision_key"))
}
