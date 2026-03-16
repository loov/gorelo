//go:build custom

package example

// CustomFeature is only available when built with the "custom" tag.
var CustomFeature = true

// CustomGreeting returns a greeting for the custom build.
func CustomGreeting() string {
	return "custom build"
}
