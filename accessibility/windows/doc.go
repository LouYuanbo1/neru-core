// Package windows is the Windows accessibility backend, built on UI
// Automation.
//
// The tree is read through the UI Automation client API over raw COM vtable
// calls, so the package is pure Go: no CGo, no generated wrappers, and nothing
// outside the standard library. Roles are UIA control-type programmatic names
// ("Button", "Edit", "ListItem"), which is the native role vocabulary the ax
// contract asks this platform for; the localized control type is never used,
// because it changes with the system language.
//
// An enumerated Node holds no COM reference. Every property is copied into a
// plain Go value before the object behind it is released, so a node can outlive
// the query that produced it and can cross goroutines. The UIA client object
// itself is created per query and released before the query returns, because
// COM objects belong to an apartment and a goroutine is not tied to one thread.
//
// Actions are injected as mouse input at a point through SendInput rather than
// invoked through UIA patterns, because the ax contract describes a click on
// the screen: the same click has to work on a control with no Invoke pattern,
// and it has to carry the modifiers the caller asked for.
package windows
