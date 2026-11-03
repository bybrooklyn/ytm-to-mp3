package export

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDescribe(t *testing.T) {
	p := filepath.Join(t.TempDir(), "takeout-1.zip")
	os.WriteFile(p, make([]byte, 3<<20), 0o600)
	old := time.Now().Add(-2 * time.Hour)
	os.Chtimes(p, old, old)
	got := Describe(p)
	if !strings.Contains(got, "takeout-1.zip (3.0 MB, modified 2h ago)") {
		t.Errorf("got %q", got)
	}
}
