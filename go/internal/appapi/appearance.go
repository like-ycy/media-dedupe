package appapi

import "media-dedupe/internal/appearance"

// GetSystemAppearance reads macOS appearance; other platforms use the WebView media query.
func (a *App) GetSystemAppearance() string {
	return appearance.Get()
}
