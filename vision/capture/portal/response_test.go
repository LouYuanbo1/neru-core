//go:build linux

package portal

import (
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
)

func TestResponseError_SeesEveryNonZeroCodeAsADenial(t *testing.T) {
	tests := []struct {
		name     string
		code     uint32
		wantCode errs.Code
	}{
		{name: "success is no error", code: responseSuccess, wantCode: ""},
		{name: "cancelled is a denial", code: responseCanceled, wantCode: errs.CodeAccessibilityDenied},
		{name: "the backend failing is a denial", code: responseFailed, wantCode: errs.CodeAccessibilityDenied},
		{name: "an undefined code is not a success", code: 7, wantCode: errs.CodeAccessibilityDenied},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := responseError(test.code)
			if test.wantCode == "" {
				if err != nil {
					t.Fatalf("responseError(%d) = %v, want nil", test.code, err)
				}

				return
			}

			if !errs.IsCode(err, test.wantCode) {
				t.Errorf("responseError(%d) error = %v, want code %s", test.code, err, test.wantCode)
			}
		})
	}
}
