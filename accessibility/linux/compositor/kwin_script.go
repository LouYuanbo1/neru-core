package compositor

// The KWin script, kept apart from the Go that receives what it sends.

// kwinGeometryScript is loaded into KWin. It reports the focused window's client
// geometry (content rectangle, excluding the title bar, so it aligns 1:1 with the
// AT-SPI content origin) and its identity.
//
// It is driven by three families of signals:
//
//   - activation (workspace.windowActivated) says which window to report on.
//   - geometry (clientGeometryChanged, frameGeometryChanged and
//     interactiveMoveResizeFinished) covers a window moved, resized, tiled or
//     maximized without losing focus.
//   - disappearance (workspace.windowRemoved, minimizedChanged, and activation
//     going to nothing) lets the cache empty, so a closed window's rectangle is
//     not reported after it is gone.
//
// Three details are load-bearing and easy to undo by accident:
//
//   - The desktop is tested before kwinTransient, not after. KDE's desktop is a
//     plasmashell window and isSpecialWindow() includes isDesktop(), so
//     kwinTransient would swallow it and the clear would never fire.
//   - Every property reaching the payload goes through kwinStr. An absent KWin
//     property concatenates as "undefined", which the Go side would read as a
//     disagreeing identity and reject this window's origin all session.
//   - A window that stops being reportable re-reads who is active rather than
//     clearing outright (kwinRefocus). Minimizing a background window must not
//     empty a cache that still describes the focused one.
//
// kwinTransient filters surfaces that briefly take activation but are never hint
// targets: KWin's non-application window types plus a few transient classes (the
// XWayland video bridge, plasmashell, the portal consent dialog). Focus landing on
// one keeps the last real window cached, or hint clicks would mis-offset. Absent
// KWin properties and signals are undefined, so the extra type flags and guarded
// connects are safe across KWin versions.
const kwinGeometryScript = `
function kwinStr(v) {
    return (v === undefined || v === null) ? "" : "" + v;
}
function kwinTransient(c) {
    if (kwinStr(c.resourceClass) == "neru") return true;
    if (c.specialWindow || c.dock || c.splash ||
        c.utility || c.toolbar || c.menu || c.dropdownMenu || c.popupMenu ||
        c.tooltip || c.notification || c.criticalNotification ||
        c.onScreenDisplay || c.comboBox || c.dndIcon) return true;
    var cls = kwinStr(c.resourceClass).toLowerCase();
    if (cls == "xwaylandvideobridge" || cls == "plasmashell" ||
        cls == "org.kde.plasmashell" ||
        cls == "org.freedesktop.impl.portal.desktop.kde") return true;
    return false;
}
var kwinLast = "";
function kwinCall(method, payload) {
    callDBus("org.neru.core.KWinBridge", "/org/neru/core/KWinBridge",
             "org.neru.core.KWinBridge", method, payload);
}
function kwinClear(reason) {
    if (kwinLast === "") return;
    kwinLast = "";
    kwinCall("ClearActiveWindow", reason);
}
function kwinPush(c) {
    var g = c.clientGeometry ? c.clientGeometry : c.frameGeometry;
    if (!g) return;
    var payload = "" + Math.round(g.x) + "," + Math.round(g.y) + "," +
                  Math.round(g.width) + "," + Math.round(g.height) + "," +
                  kwinStr(c.resourceClass) + "," + kwinStr(c.resourceName) + "," +
                  kwinStr(c.caption);
    if (payload === kwinLast) return;
    kwinLast = payload;
    kwinCall("UpdateActiveWindow", payload);
}
function kwinReportable(c) {
    return c.active && !c.desktopWindow && !kwinTransient(c);
}
function kwinActivated(c) {
    if (!c) { kwinClear("unfocused"); return; }
    if (c.desktopWindow) { kwinClear("desktop"); return; }
    if (kwinTransient(c)) return;
    kwinPush(c);
}
function kwinRefocus(gone, reason) {
    var active = workspace.activeWindow;
    if (!active || active === gone) { kwinClear(reason); return; }
    kwinActivated(active);
}
function kwinWatch(c) {
    if (!c) return;
    var onGeometry = function () {
        if (c.move || c.resize) return;
        if (!kwinReportable(c)) return;
        kwinPush(c);
    };
    if (c.clientGeometryChanged) c.clientGeometryChanged.connect(onGeometry);
    if (c.frameGeometryChanged) c.frameGeometryChanged.connect(onGeometry);
    if (c.interactiveMoveResizeFinished)
        c.interactiveMoveResizeFinished.connect(function () {
            if (kwinReportable(c)) kwinPush(c);
        });
    if (c.minimizedChanged)
        c.minimizedChanged.connect(function () {
            if (c.minimized) kwinRefocus(c, "minimized");
        });
}
workspace.windowAdded.connect(kwinWatch);
workspace.windowActivated.connect(kwinActivated);
workspace.windowRemoved.connect(function (c) { kwinRefocus(c, "closed"); });
var kwinOpen = workspace.stackingOrder;
for (var kwinI = 0; kwinI < kwinOpen.length; kwinI++) kwinWatch(kwinOpen[kwinI]);
kwinActivated(workspace.activeWindow);
`
