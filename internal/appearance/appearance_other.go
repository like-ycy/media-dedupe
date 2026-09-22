//go:build !darwin

package appearance

// Get 返回空字符串，表示请前端改用 prefers-color-scheme。
func Get() string {
	return ""
}
