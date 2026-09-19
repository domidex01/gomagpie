// Package buildmod is a fixture third-party module for build tests: its
// init proves third-party registration executed inside a custom binary via
// the marker-file protocol ($MAGPIE_BUILD_TEST_MARKER gains a line).
package buildmod

import (
	"os"

	"github.com/motherlodelab/magpie/core"
)

func init() {
	core.MustRegister(core.ModuleInfo{
		ID:         "test.buildmod",
		APIVersion: "1.0.0",
		Kind:       core.KindExporter,
		New:        func() any { return nil },
	})
	if marker := os.Getenv("MAGPIE_BUILD_TEST_MARKER"); marker != "" {
		f, err := os.OpenFile(marker, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.WriteString("buildmod init\n")
			_ = f.Close()
		}
	}
}
