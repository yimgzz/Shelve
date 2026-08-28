//go:build !ios

// Package main stub: the wails template's build/ios scaffolding compiles as
// a main package on non-iOS hosts (app_options_default.go is tagged !ios),
// which breaks plain `go build ./...`. The real entry point is
// main_ios.go, compiled only for GOOS=ios by the Xcode task.
package main

func main() {}
