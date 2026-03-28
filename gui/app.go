package main

import (
	"image/color"

	"fyne.io/fyne/v2"
	fyneApp "fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"

	"termd/frontend/client"
	termlog "termd/frontend/log"
)

type guiApp struct {
	fyneApp   fyne.App
	window    fyne.Window
	session   *session
	terminal  *TerminalWidget
	statusBar *canvas.Text
	logRing   *termlog.LogRingBuffer
	endpoint  string
	version   string
	changelog string
}

func newApp(c *client.Client, shell string, shellArgs []string, logRing *termlog.LogRingBuffer, endpoint, version, changelog string) *guiApp {
	sess := newSession(c, shell, shellArgs)

	a := &guiApp{
		fyneApp:   fyneApp.New(),
		session:   sess,
		logRing:   logRing,
		endpoint:  endpoint,
		version:   version,
		changelog: changelog,
	}

	a.window = a.fyneApp.NewWindow("termd-gui")
	a.terminal = NewTerminalWidget(sess)
	a.terminal.onDetach = func() {
		a.window.Close()
	}

	// Status bar at top
	a.statusBar = canvas.NewText("connecting...", color.RGBA{0x88, 0x88, 0x88, 0xff})
	a.statusBar.TextSize = 12
	a.statusBar.TextStyle = fyne.TextStyle{Monospace: true}

	// Status bar background
	statusBG := canvas.NewRectangle(color.RGBA{0x30, 0x30, 0x30, 0xff})
	statusContainer := container.NewStack(statusBG, container.New(layout.NewPaddedLayout(), a.statusBar))

	content := container.NewBorder(statusContainer, nil, nil, nil, a.terminal)
	a.window.SetContent(content)
	a.window.Resize(fyne.NewSize(
		a.terminal.cellWidth*80+10,
		a.terminal.cellHeight*24+a.statusBar.MinSize().Height+20,
	))

	// Wire up session callbacks
	sess.onUpdate = func() {
		a.terminal.updateFromSession()
	}
	sess.onStatus = func(status string) {
		a.updateStatusBar()
	}

	// Focus the terminal widget
	a.window.Canvas().Focus(a.terminal)

	return a
}

func (a *guiApp) run() {
	go a.session.run()
	a.updateStatusBar()
	a.window.ShowAndRun()
}

func (a *guiApp) updateStatusBar() {
	name := a.session.getRegionName()
	status := a.session.getConnStatus()
	text := a.endpoint
	if name != "" {
		text = name + " | " + text
	}
	text += " | " + status
	a.statusBar.Text = text
	a.statusBar.Refresh()
}
