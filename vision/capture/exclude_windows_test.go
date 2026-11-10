//go:build windows

package capture

import (
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
)

func TestExcludeWindow_RejectsAnEmptyHandle(t *testing.T) {
	t.Parallel()

	if err := ExcludeWindow(0); !errs.IsCode(err, errs.CodeInvalidInput) {
		t.Errorf("ExcludeWindow(0) = %v, want CodeInvalidInput", err)
	}
}

func TestExcludeOwnWindows_IsSafeForAWindowlessProcess(t *testing.T) {
	t.Parallel()

	// The test binary owns no top-level window, so this marks nothing and must
	// still report success; a caller should be able to invoke it unconditionally.
	if err := ExcludeOwnWindows(); err != nil {
		t.Errorf("ExcludeOwnWindows() = %v, want nil for a process with no windows", err)
	}

	if err := ExcludeOwnWindows(); err != nil {
		t.Errorf("second ExcludeOwnWindows() = %v, want nil", err)
	}
}
