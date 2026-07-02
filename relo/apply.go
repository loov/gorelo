package relo

import (
	"fmt"
	"os"
	"path/filepath"
)

// applyPlan writes all file edits to disk (phase 9).
func applyPlan(plan *Plan) error {
	for _, fe := range plan.Edits {
		if fe.IsDelete {
			if err := os.Remove(fe.Path); err != nil {
				return fmt.Errorf("deleting %q: %w", fe.Path, err)
			}
			continue
		}

		if fe.IsNew {
			dir := filepath.Dir(fe.Path)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("creating directory %q: %w", dir, err)
			}
		}

		if err := os.WriteFile(fe.Path, []byte(fe.Content), 0644); err != nil {
			return fmt.Errorf("writing %q: %w", fe.Path, err)
		}
	}
	return nil
}
