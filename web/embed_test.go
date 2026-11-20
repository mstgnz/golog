package web

import (
	"io/fs"
	"testing"
)

func TestStaticHasDashboardFiles(t *testing.T) {
	for _, name := range []string{"index.html", "app.js", "styles.css", "favicon.svg"} {
		if _, err := fs.Stat(Static(), name); err != nil {
			t.Errorf("embedded dashboard is missing %s: %v", name, err)
		}
	}
}
