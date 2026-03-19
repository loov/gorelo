package relo

// Consumer package rewriting is handled as part of phase 8 (assemble)
// when Options.RewriteConsumers is enabled.
//
// The implementation walks the module tree looking for Go files that import
// source packages with moved declarations and rewrites their selector
// expressions to use the new target package.
//
// This is deferred to a future iteration as it requires filesystem walking
// and is optional (controlled by Options.RewriteConsumers).
