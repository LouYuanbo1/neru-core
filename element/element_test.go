package element_test

import (
	"image"
	"testing"

	"github.com/LouYuanbo1/neru-core/element"
)

func TestNewElement(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		id      element.ID
		bounds  image.Rectangle
		role    element.Role
		opts    []element.Option
		wantErr bool
	}{
		{
			name:   "valid element",
			id:     "test-1",
			bounds: image.Rect(10, 10, 100, 50),
			role:   element.RoleButton,
			opts:   []element.Option{element.WithClickable(true)},
		},
		{
			name:    "empty ID is refused",
			id:      "",
			bounds:  image.Rect(10, 10, 100, 50),
			role:    element.RoleButton,
			wantErr: true,
		},
		{
			name:    "empty bounds are refused",
			id:      "test-2",
			bounds:  image.Rectangle{},
			role:    element.RoleButton,
			wantErr: true,
		},
		{
			name:   "title and description",
			id:     "test-3",
			bounds: image.Rect(0, 0, 50, 30),
			role:   element.RoleLink,
			opts: []element.Option{
				element.WithTitle("Click me"),
				element.WithDescription("A clickable link"),
				element.WithClickable(true),
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			elem, err := element.NewElement(testCase.id, testCase.bounds, testCase.role, testCase.opts...)

			if testCase.wantErr {
				if err == nil {
					t.Error("NewElement() expected an error, got nil")
				}

				return
			}

			if err != nil {
				t.Fatalf("NewElement(): %v", err)
			}

			if elem.ID() != testCase.id {
				t.Errorf("ID() = %q, want %q", elem.ID(), testCase.id)
			}

			if elem.Bounds() != testCase.bounds {
				t.Errorf("Bounds() = %v, want %v", elem.Bounds(), testCase.bounds)
			}

			if elem.Role() != testCase.role {
				t.Errorf("Role() = %q, want %q", elem.Role(), testCase.role)
			}
		})
	}
}

func TestElement_Geometry(t *testing.T) {
	t.Parallel()

	elem, err := element.NewElement("test", image.Rect(10, 20, 110, 70), element.RoleButton)
	if err != nil {
		t.Fatalf("NewElement(): %v", err)
	}

	if got, want := elem.Center(), (image.Point{X: 60, Y: 45}); got != want {
		t.Errorf("Center() = %v, want %v", got, want)
	}

	contains := []struct {
		point image.Point
		want  bool
	}{
		{image.Point{X: 50, Y: 30}, true},
		{image.Point{X: 10, Y: 10}, false}, // outside: the rect starts at y=20
		{image.Point{X: 5, Y: 30}, false},
		{image.Point{X: 115, Y: 30}, false},
	}

	for _, testCase := range contains {
		if got := elem.Contains(testCase.point); got != testCase.want {
			t.Errorf("Contains(%v) = %v, want %v", testCase.point, got, testCase.want)
		}
	}

	if !elem.IsVisible(image.Rect(0, 0, 100, 100)) {
		t.Error("an element inside the screen must be visible")
	}

	if elem.IsVisible(image.Rect(200, 200, 300, 300)) {
		t.Error("an element off screen must not be visible")
	}
}

func TestElement_Overlaps(t *testing.T) {
	t.Parallel()

	first, err := element.NewElement("first", image.Rect(10, 10, 50, 50), element.RoleButton)
	if err != nil {
		t.Fatalf("NewElement(): %v", err)
	}

	tests := []struct {
		name   string
		bounds image.Rectangle
		want   bool
	}{
		{name: "contained", bounds: image.Rect(20, 20, 40, 40), want: true},
		{name: "partial", bounds: image.Rect(40, 40, 80, 80), want: true},
		{name: "touching edge", bounds: image.Rect(50, 10, 90, 50), want: false},
		{name: "separate", bounds: image.Rect(60, 60, 100, 100), want: false},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			second, err := element.NewElement("second", testCase.bounds, element.RoleButton)
			if err != nil {
				t.Fatalf("NewElement(): %v", err)
			}

			if got := first.Overlaps(second); got != testCase.want {
				t.Errorf("Overlaps() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestElement_Text_PrefersValueThenTitleThenDescription(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts []element.Option
		want string
	}{
		{
			name: "a field's value wins over its label",
			opts: []element.Option{
				element.WithTitle("Name"),
				element.WithValue("Ada"),
				element.WithDescription("Your name"),
			},
			want: "Ada",
		},
		{
			name: "a button is named by its title",
			opts: []element.Option{element.WithTitle("Save"), element.WithDescription("Saves the file")},
			want: "Save",
		},
		{
			name: "an icon button may have only a description",
			opts: []element.Option{element.WithDescription("Back")},
			want: "Back",
		},
		{name: "an element with no text", opts: nil, want: ""},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			elem, err := element.NewElement("id", image.Rect(0, 0, 10, 10), element.RoleButton, testCase.opts...)
			if err != nil {
				t.Fatalf("NewElement(): %v", err)
			}

			if got := elem.Text(); got != testCase.want {
				t.Errorf("Text() = %q, want %q", got, testCase.want)
			}
		})
	}
}
