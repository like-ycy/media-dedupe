package video

import (
	"errors"
	"strings"
	"testing"
)

func TestToolFailureMarksStorageDiagnostics(t *testing.T) {
	cause := errors.New("exit status 1")
	for _, stderr := range []string{
		"Input/output error",
		"[mov,mp4] moov atom not found\nInput/output error",
		"av_interleaved_write_frame(): No space left on device",
		"Read-only file system",
		"Too many open files",
	} {
		err := toolFailure("ffprobe", stderr, cause)
		if !errors.Is(err, ErrStorageIO) {
			t.Fatalf("stderr %q not classified as storage error: %v", stderr, err)
		}
		if !strings.Contains(err.Error(), "storage") {
			t.Fatalf("message lost the diagnostic: %v", err)
		}
	}
}

func TestToolFailureKeepsMediaErrorsPerFile(t *testing.T) {
	err := toolFailure("ffprobe", "moov atom not found\nInvalid data found when processing input", errors.New("exit status 1"))
	if errors.Is(err, ErrStorageIO) {
		t.Fatalf("corrupt media must stay a per-file error: %v", err)
	}
	if !strings.Contains(err.Error(), "moov atom not found") {
		t.Fatalf("diagnostic not preserved: %v", err)
	}
}
