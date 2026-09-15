package main

import "media-dedupe/internal/appmain"

// Desktop app entry for wails build (root).
// The same binary logic is also exposed via cmd/app.
func main() {
	appmain.Run()
}
