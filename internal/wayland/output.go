//go:build linux

package wayland

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/LouYuanbo1/neru-core/errs"
)

// Output is one compositor output: where it sits on the desktop layout and how
// many frame pixels one logical pixel of it contains.
type Output struct {
	// GlobalName is the wl_registry name of the wl_output global. It identifies
	// the output for the lifetime of the compositor session and is what
	// CaptureOutput matches on.
	GlobalName int
	// X and Y are the output's top-left corner in the compositor's desktop
	// layout, in logical pixels. They can be negative.
	X, Y int
	// Width and Height are the output's logical size: its current mode divided
	// by Scale, rounded to the nearest logical pixel.
	Width, Height int
	// Scale is the wl_output scale factor: frame pixels per logical pixel. It is
	// at least 1.
	Scale int
	// Name is the make and model the compositor reported, for diagnostics. It is
	// never empty: an output that reports neither is named by its global id.
	Name string
}

// discoveryTimeout bounds Outputs, which has no context of its own. It matches
// the budget a capture caller applies: a local compositor answers both
// roundtrips in well under a millisecond.
const discoveryTimeout = 2 * time.Second

// outputState accumulates the wl_output events for one bound output. All fields
// are written from a single method goroutine, because every exchange on a Conn
// is serialized.
type outputState struct {
	globalName int
	wlName     uint32
	transform  int

	x, y          int
	width, height int
	scale         int

	hasGeometry bool
	hasMode     bool
	done        bool

	make, model string
}

// apply consumes one event addressed to this output.
func (o *outputState) apply(ev event) error {
	if ev.object != o.wlName {
		return nil
	}

	switch ev.opcode {
	case outputEventGeometry:
		d := decoder{buf: ev.body}

		x, err := d.int32()
		if err != nil {
			return err
		}

		y, err := d.int32()
		if err != nil {
			return err
		}

		// Physical width, physical height and subpixel are not needed.
		for range 3 {
			if _, err := d.int32(); err != nil {
				return err
			}
		}

		makeName, err := d.string()
		if err != nil {
			return err
		}

		modelName, err := d.string()
		if err != nil {
			return err
		}

		transform, err := d.int32()
		if err != nil {
			return err
		}

		o.x, o.y = int(x), int(y)
		o.make, o.model = makeName, modelName
		o.transform = int(transform)
		o.hasGeometry = true
	case outputEventMode:
		values, err := decodeUint32s(ev.body, 4)
		if err != nil {
			return err
		}

		flags, width, height := values[0], values[1], values[2]

		// Only the current mode describes the output; the others are extra
		// resolutions the compositor happens to advertise.
		isCurrent := flags&outputModeCurrent != 0

		if isCurrent || !o.hasMode {
			o.width, o.height = int(width), int(height)
			o.hasMode = true
		}
	case outputEventScale:
		values, err := decodeUint32s(ev.body, 1)
		if err != nil {
			return err
		}

		o.scale = max(int(int32(values[0])), 1)
	case outputEventDone:
		o.done = true
	}

	return nil
}

// name is the diagnostic label for this output.
func (o *outputState) name() string {
	name := strings.TrimSpace(o.make + " " + o.model)
	if name == "" {
		return fmt.Sprintf("output-%d", o.globalName)
	}

	return name
}

// snapshot converts the accumulated state into the exported view. An output
// whose mode was never reported cannot describe a size, so it is omitted.
func (o *outputState) snapshot() (Output, bool) {
	if !o.hasMode || o.width <= 0 || o.height <= 0 {
		return Output{}, false
	}

	scale := max(o.scale, 1)

	return Output{
		GlobalName: o.globalName,
		X:          o.x,
		Y:          o.y,
		Width:      (o.width + scale/2) / scale,
		Height:     (o.height + scale/2) / scale,
		Scale:      scale,
		Name:       o.name(),
	}, true
}

// checkOutputCapturable refuses an output whose pixels would arrive in an
// orientation this API cannot describe.
func checkOutputCapturable(o *outputState) error {
	if o.transform != outputTransformNormal {
		return errs.Newf(
			errs.CodeNotSupported,
			"output %q is rotated (wl_output transform %d); wlr-screencopy returns the frame in buffer orientation and this API cannot describe the rotation",
			o.name(),
			o.transform,
		)
	}

	return nil
}

// Outputs returns the outputs the compositor advertises, with their layout
// geometry, logical size and scale. It binds wl_shm and wlr-screencopy as a
// side effect and caches them, because a capture needs both; a compositor that
// lacks either makes Outputs itself fail with CodeNotSupported.
//
// The result is sorted by wl_registry global name, which is the compositor's
// enumeration order, so the first output is stable for a session. Wayland has
// no primary output; callers that capture "the screen" pick one from here.
func (c *Conn) Outputs() ([]Output, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), discoveryTimeout)
	defer cancel()

	if err := c.discover(ctx); err != nil {
		return nil, err
	}

	return c.snapshotOutputs(), nil
}

// snapshotOutputs copies the cached output states into exported values.
func (c *Conn) snapshotOutputs() []Output {
	outputs := make([]Output, 0, len(c.outputs))

	for _, state := range c.outputs {
		snapshot, ok := state.snapshot()
		if ok {
			outputs = append(outputs, snapshot)
		}
	}

	return outputs
}

// discover binds the globals a capture needs and reads every output's initial
// event burst. It runs once per Conn; both the success and the failure are
// cached so a second call does not bind a second registry.
func (c *Conn) discover(ctx context.Context) error {
	if c.discovered {
		return nil
	}

	if c.discoverErr != nil {
		return c.discoverErr
	}

	if err := c.runDiscovery(ctx); err != nil {
		c.discoverErr = err

		return err
	}

	c.discovered = true

	return nil
}

// runDiscovery performs the two roundtrips discovery needs: one to enumerate
// globals, one to receive each bound object's initial events.
func (c *Conn) runDiscovery(ctx context.Context) error {
	registry := c.newID()
	c.registry = registry
	c.register(registry)

	var body encoder
	body.uint32(registry)

	if err := c.send(ctx, displayID, displayOpGetRegistry, body.buf); err != nil {
		return err
	}

	var globals []registryGlobal

	err := c.roundtrip(ctx, func(ev event) error {
		if ev.object != registry || ev.opcode != registryEventGlobal {
			return nil
		}

		global, err := parseRegistryGlobal(ev.body)
		if err != nil {
			return err
		}

		globals = append(globals, global)

		return nil
	})
	if err != nil {
		return err
	}

	states := make(map[uint32]*outputState)

	for _, global := range globals {
		switch global.iface {
		case interfaceWLShm:
			if c.shm != 0 {
				continue
			}

			id := c.newID()
			c.register(id)

			if err := c.bind(ctx, registry, global.name, interfaceWLShm, shmVersion, id); err != nil {
				return err
			}

			c.shm = id
			c.shmFormats = make(map[uint32]bool)
		case interfaceScreencopy:
			if c.screencopy != 0 {
				continue
			}

			version := min(global.version, screencopyManagerVersion)
			id := c.newID()
			c.register(id)

			if err := c.bind(ctx, registry, global.name, interfaceScreencopy, version, id); err != nil {
				return err
			}

			c.screencopy = id
			c.screencopyVersion = version
		case interfaceWLOutput:
			version := min(global.version, outputVersion)
			if version == 0 {
				continue
			}

			id := c.newID()
			c.register(id)

			state := &outputState{globalName: int(global.name), wlName: id, scale: 1}
			states[id] = state

			if err := c.bind(ctx, registry, global.name, interfaceWLOutput, version, id); err != nil {
				return err
			}
		}
	}

	if c.shm == 0 {
		return errs.New(
			errs.CodeNotSupported,
			"the compositor does not advertise wl_shm, so a screencopy buffer cannot be created",
		)
	}

	if c.screencopy == 0 {
		return errs.New(
			errs.CodeNotSupported,
			"the compositor does not implement wlr-screencopy-unstable-v1, the protocol this capture uses",
		)
	}

	// A second roundtrip flushes the binds and collects the output geometry,
	// mode, scale and done events, plus wl_shm's format list.
	err = c.roundtrip(ctx, func(ev event) error {
		if ev.object == c.shm && ev.opcode == shmEventFormat {
			values, err := decodeUint32s(ev.body, 1)
			if err != nil {
				return err
			}

			c.shmFormats[values[0]] = true

			return nil
		}

		if state, ok := states[ev.object]; ok {
			return state.apply(ev)
		}

		return nil
	})
	if err != nil {
		return err
	}

	c.outputs = make([]*outputState, 0, len(states))
	for _, state := range states {
		c.outputs = append(c.outputs, state)
	}

	sort.Slice(c.outputs, func(i, j int) bool {
		return c.outputs[i].globalName < c.outputs[j].globalName
	})

	return nil
}

// bind sends wl_registry.bind for one global.
func (c *Conn) bind(
	ctx context.Context,
	registry, name uint32,
	iface string,
	version, id uint32,
) error {
	var body encoder
	body.uint32(name)
	body.string(iface)
	body.uint32(version)
	body.uint32(id)

	return c.send(ctx, registry, registryOpBind, body.buf)
}

// findOutput resolves the Output a caller passed back to its bound state. The
// global name is authoritative; a caller that built an Output from scratch may
// instead rely on the geometry matching.
func (c *Conn) findOutput(want Output) *outputState {
	for _, state := range c.outputs {
		if state.globalName == want.GlobalName {
			return state
		}
	}

	if want.GlobalName != 0 {
		return nil
	}

	for _, state := range c.outputs {
		snapshot, ok := state.snapshot()
		if ok && snapshot.X == want.X && snapshot.Y == want.Y &&
			snapshot.Width == want.Width && snapshot.Height == want.Height {
			return state
		}
	}

	return nil
}

// outputByGlobalName finds the bound state for a snapshot's global name.
func (c *Conn) outputByGlobalName(name int) *outputState {
	for _, state := range c.outputs {
		if state.globalName == name {
			return state
		}
	}

	return nil
}

// supportsShmFormat reports whether wl_shm advertised format.
func (c *Conn) supportsShmFormat(format uint32) bool {
	return c.shmFormats[format]
}
