// Package contour finds interactive UI target bounding boxes in a screen
// frame using an algorithm ported from wl-kbptr: grayscale, 5x5 Gaussian
// blur, Sobel, non-maximum suppression, Canny hysteresis, scale-dependent
// dilation, connected components and hierarchical filtering. It is pure Go and
// platform-independent.
package contour

// DefaultParams returns the detector's shipped thresholds. They are the values
// the project tunes against real interfaces: edges from a mid-strength Sobel
// hysteresis, targets at least 7x3 logical pixels and no larger than 650x160,
// menu strokes no taller than 6, containers from 50 logical pixels up, nested
// duplicates within 8 pixels of their parent's center, and square icons up to
// 40 pixels with 5 pixels of slack.
//
// A caller that wants a different bias copies the struct and changes the
// fields it understands; the zero value filters everything out and is not a
// usable configuration.
func DefaultParams() Params {
	return Params{
		EdgeLowThreshold:  70,
		EdgeHighThreshold: 220,
		MinTargetWidth:    7,
		MinTargetHeight:   3,
		MaxTargetWidth:    650,
		MaxTargetHeight:   160,
		FlatLineHeight:    6,
		ContainerHeight:   50,
		SameCenterSlack:   8,
		SquareIconSize:    40,
		SquareIconSlack:   5,
	}
}

// Params are the detector's tunable numbers. The detector takes whatever it
// is handed; validation happens at the call site or config load.
//
// Thresholds are Sobel gradient magnitudes on a 0..255 luma frame. Sizes are
// logical pixels, after dividing by the frame scale.
type Params struct {
	// EdgeLowThreshold and EdgeHighThreshold are the Canny hysteresis pair:
	// pixels above high start an edge, pixels above low extend one.
	EdgeLowThreshold  int
	EdgeHighThreshold int

	// Blobs outside these bounds are noise (below) or layout containers (above).
	MinTargetWidth  float64
	MinTargetHeight float64
	MaxTargetWidth  float64
	MaxTargetHeight float64

	// FlatLineHeight drops nested strokes no taller than this (menu lines).
	FlatLineHeight float64
	// ContainerHeight is where a blob starts counting as a card or dialog
	// whose children are the real targets.
	ContainerHeight float64
	// SameCenterSlack is how close to its parent's center a nested blob
	// must be to count as a duplicate of it.
	SameCenterSlack float64
	// SquareIconSize and SquareIconSlack describe a roughly square parent
	// small enough that its inner detail is the icon's own artwork.
	SquareIconSize  float64
	SquareIconSlack float64
}
