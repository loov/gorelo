package relo

import (
	"fmt"
	"go/token"
	"path/filepath"
)

// validate.go holds POST-RESOLUTION validators. Every function here
// takes []*resolvedRelo (or a derived map keyed by *resolvedRelo) so
// that its decisions see the full resolved move/rename set and the
// expanded file-move relos. This is the enforcement boundary for the
// bug class behind 6f72da8 and d39ff98: a naive per-rule check that
// only looks at one relo at a time loses information about concurrent
// file moves, renames, and attach/detach operations in the same run.
//
// Pre-resolution validation (per-relo kind checks, identifier validity,
// dedup) lives in resolve.go. The per-relo pipeline there uses the raw
// []Relo input and does NOT have access to []*resolvedRelo — by design.
//
// A lint test (TestPostResolutionValidatorsTakeResolved in
// validate_lint_test.go) scans this file and conflict.go to ensure the
// convention cannot silently drift: any function named check*/detect*/
// validate* here must have []*resolvedRelo in its signature.

// checkUnexportedCrossPkg rejects cross-package moves of unexported
// declarations that have references outside the move set. A reference
// inside a file that is being file-moved to the same destination
// directory travels along with the def and does not count as external.
//
// Post-resolution because the decision depends on the full resolved
// set plus the file-move plan (fmInfos): a per-relo check cannot see
// which other files are riding along to the same directory.
func checkUnexportedCrossPkg(resolved []*resolvedRelo, fmInfos []*fileMoveInfo) error {
	fileMoveTargetDir := make(map[string]string, len(fmInfos))
	for _, info := range fmInfos {
		fileMoveTargetDir[info.move.From] = filepath.Dir(info.move.To)
	}
	for _, rr := range resolved {
		// Synthesized relos (auto-added methods for a moving type)
		// are handled by synthesize(): unexported methods that are
		// only called from sibling methods of the same type stay
		// unexported on purpose. Skip them so the check keeps its
		// original user-facing scope.
		if rr.Synthesized {
			continue
		}
		if !rr.isCrossPackageMove() {
			continue
		}
		if token.IsExported(rr.TargetName) {
			continue
		}
		if !hasExternalUses(rr.Group, finalDir(rr), fileMoveTargetDir) {
			continue
		}
		return fmt.Errorf("unexported name %q cannot be moved cross-package without a rename to an exported name", rr.Group.Name)
	}
	return nil
}
