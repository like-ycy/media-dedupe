//go:build darwin

package appearance

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

const char* getSystemAppearance(void) {
	@autoreleasepool {
		// AppleInterfaceStyle 仅在深色模式下被写入
		NSString *mode = [[NSUserDefaults standardUserDefaults] stringForKey:@"AppleInterfaceStyle"];
		if (mode != nil && [mode isEqualToString:@"Dark"]) {
			return "dark";
		}
		return "light";
	}
}
*/
import "C"

// Get 返回 macOS 当前系统外观："dark" 或 "light"。
func Get() string {
	return C.GoString(C.getSystemAppearance())
}
