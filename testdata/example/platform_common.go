package example

import "fmt"

// Uses PlatformName() which is defined separately per OS.
func Greeting() string {
	return "Hello from " + PlatformName()
}

// Method on platform-specific File type; uses the common Name field.
func (f *File) PrintName() {
	fmt.Println(f.Name)
}
