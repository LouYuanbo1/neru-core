package uinput

// scrollPixelsPerNotch is how many pixels of a caller's scroll delta make one
// wheel notch. It is the same 30 px/notch ratio the X11 path this library
// already has uses, so one binding travels the same distance whichever Linux
// backend answers it.
const scrollPixelsPerNotch = 30

// notchAccumulator turns a stream of pixel deltas into whole wheel notches.
//
// A caller animating a scroll sends fractions of a notch, and a wheel event
// cannot: REL_WHEEL is a whole notch. So the pixels are accumulated and only
// whole notches are emitted, with the remainder carried into the next call.
// That makes the total travelled distance exact over a stream while keeping a
// single call cheap, and it means a call smaller than the remainder emits
// nothing rather than rounding up and over-scrolling.
//
// Division truncates toward zero in Go, which is what this needs: a remainder
// of either sign stays smaller than one notch in magnitude and the next delta
// in the opposite direction cancels it.
type notchAccumulator struct {
	pixels int
}

// add records delta pixels and returns the whole notches that are now due. The
// sign is the caller's: a positive delta yields positive notches.
func (a *notchAccumulator) add(delta int) int {
	a.pixels += delta

	notches := a.pixels / scrollPixelsPerNotch
	a.pixels -= notches * scrollPixelsPerNotch

	return notches
}
