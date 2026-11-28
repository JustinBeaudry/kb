package migrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The schema runner splits migrations on ";", so a semicolon in a comment
// would cut a statement in two and fail every schema initialization.
func TestMigrationCommentsHaveNoStatementSeparator(t *testing.T) {
	files, err := filepath.Glob("*.cypher")
	if err != nil || len(files) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") && strings.Contains(line, ";") {
				t.Errorf("%s:%d: a comment holds ';', which the schema runner reads as the end of a statement", file, i+1)
			}
		}
	}
}
