//go:build darwin

package visionframework

import (
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
)

func TestRectanglesOptionsValidate_AcceptsZeroAndInRange(t *testing.T) {
	cases := []struct {
		name string
		opts RectanglesOptions
	}{
		{name: "the zero value leaves everything to Vision", opts: RectanglesOptions{}},
		{name: "a full image as the minimum size", opts: RectanglesOptions{MinimumSize: 1}},
		{name: "square boxes only", opts: RectanglesOptions{MinimumAspectRatio: 1, MaximumAspectRatio: 1}},
		{name: "a widest ratio without a narrowest one", opts: RectanglesOptions{MaximumAspectRatio: 20}},
		{name: "one candidate", opts: RectanglesOptions{MaximumObservations: 1}},
		{name: "the largest tolerance", opts: RectanglesOptions{QuadratureTolerance: 45}},
		{name: "the options Boxes asks for", opts: boxesOptions()},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.opts.validate(); err != nil {
				t.Fatalf("validate(%+v) = %v, want nil", testCase.opts, err)
			}
		})
	}
}

func TestRectanglesOptionsValidate_RefusesValuesVisionWouldReject(t *testing.T) {
	cases := []struct {
		name string
		opts RectanglesOptions
	}{
		{name: "a minimum size above the whole image", opts: RectanglesOptions{MinimumSize: 1.5}},
		{name: "a negative minimum size", opts: RectanglesOptions{MinimumSize: -0.1}},
		{name: "a negative minimum ratio", opts: RectanglesOptions{MinimumAspectRatio: -1}},
		{name: "a negative maximum ratio", opts: RectanglesOptions{MaximumAspectRatio: -2}},
		{name: "a minimum ratio above the maximum", opts: RectanglesOptions{MinimumAspectRatio: 5, MaximumAspectRatio: 2}},
		{name: "a negative candidate count", opts: RectanglesOptions{MaximumObservations: -1}},
		{name: "a tolerance past a right angle", opts: RectanglesOptions{QuadratureTolerance: 46}},
		{name: "a negative tolerance", opts: RectanglesOptions{QuadratureTolerance: -1}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.opts.validate()
			if err == nil {
				t.Fatalf("validate(%+v) = nil, want an error", testCase.opts)
			}

			if !errs.IsCode(err, errs.CodeInvalidInput) {
				t.Fatalf("validate(%+v) = %v, want code %s", testCase.opts, err, errs.CodeInvalidInput)
			}
		})
	}
}
