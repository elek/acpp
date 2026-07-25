package main

import (
	_ "embed"

	"fyne.io/systray"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// trayIcon is the ACPP "Harness" glyph (accent #47c98a), embedded so the tray
// icon works from a single self-contained binary. Copied from
// design/icons/linux/hicolor/48x48/apps/acpp.png (go:embed can't reach outside
// the module dir).
//
//go:embed appicon.png
var trayIcon []byte

// startTray shows a StatusNotifierItem (SNI/AppIndicator) tray icon for the
// running app. SNI is a D-Bus protocol, so systray.Run is safe to call off the
// main goroutine and does not contend with Wails' GTK main loop.
//
// Left-click (and the "Show" menu item) raises and focuses the window; "Quit"
// exits the whole app. The caller is responsible for calling systray.Quit() on
// shutdown so the icon disappears when the app closes.
func (a *App) startTray() {
	go systray.Run(a.onTrayReady, func() {})
}

func (a *App) onTrayReady() {
	systray.SetIcon(trayIcon)
	systray.SetTitle("ACPP")
	systray.SetTooltip("ACPP")

	systray.SetOnTapped(a.showWindow)

	show := systray.AddMenuItem("Show", "Raise and focus the ACPP window")
	quit := systray.AddMenuItem("Quit", "Quit ACPP")

	go func() {
		for {
			select {
			case <-show.ClickedCh:
				a.showWindow()
			case <-quit.ClickedCh:
				runtime.Quit(a.ctx)
				return
			}
		}
	}()
}

// showWindow brings the app window to the foreground, restoring it if it was
// minimised.
func (a *App) showWindow() {
	runtime.WindowUnminimise(a.ctx)
	runtime.WindowShow(a.ctx)
}
