//go:build windows

// Command gnodi-ai-node-tray runs the operator daemon with a tray icon instead
// of a terminal window.
//
// Same daemon, in this process rather than a child: supervising a second
// process would mean a PID file, orphans to clean up after a crash, and an
// ambiguity about whether the thing is actually running. Here there is one
// process, and closing it closes the node.
//
// Built with -H=windowsgui so no console appears, which is the whole point --
// and which is also why the log goes to a file beside the executable. A daemon
// that cannot say why it failed is worse than one that fails in a terminal.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/systray"

	"github.com/blockreigntech/gnodi-ai-node/internal/config"
	"github.com/blockreigntech/gnodi-ai-node/internal/daemon"
)

//go:embed icon.ico
var iconICO []byte

const (
	logName  = "node.log"
	pollWait = 3 * time.Second
)

func main() {
	dir := exeDir()
	closeLog := startLogging(dir)
	defer closeLog()

	// The config lives beside the executable, not in the environment. A tray
	// app is started from a shortcut or a double click, which inherits none of
	// the variables a shell would have set -- so reading node.env here is what
	// makes the icon on the taskbar work at all.
	if err := loadEnvFile(filepath.Join(dir, envName)); err != nil {
		log.Printf("config: %v", err)
	}

	app := &tray{dir: dir}
	cfg, err := config.Load()
	if err != nil {
		// Still show the icon. Exiting silently is the one behaviour that
		// leaves the operator with no way to find out what went wrong.
		app.fault = err
		log.Printf("config error: %v", err)
	} else {
		app.cfg = cfg
		app.status = "http://" + cfg.StatusAddr
	}

	systray.Run(app.onReady, app.onExit)
}

type tray struct {
	dir    string
	cfg    config.Config
	fault  error
	status string // base URL of the daemon's loopback status page

	cancel  context.CancelFunc
	done    chan struct{}
	mToggle *systray.MenuItem
	mOpen   *systray.MenuItem
}

func (t *tray) onReady() {
	systray.SetIcon(iconICO)
	systray.SetTitle("Gnodi AI Node")
	systray.SetTooltip("Gnodi AI Node - starting")

	t.mOpen = systray.AddMenuItem("Open dashboard", "The operator dashboard, on this machine only")
	t.mToggle = systray.AddMenuItem("Stop serving", "Pause and hand the GPU back")
	systray.AddSeparator()
	mLog := systray.AddMenuItem("Open log", "What the node has been doing")
	mConfig := systray.AddMenuItem("Open config folder", "Licence key and settings")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Stop serving and close")

	if t.fault != nil {
		// Nothing to open and nothing to toggle until it is configured.
		systray.SetTooltip("Gnodi AI Node - not configured")
		t.mOpen.Disable()
		t.mToggle.Disable()
	} else {
		t.start()
		go t.poll()
	}

	for {
		select {
		case <-t.mOpen.ClickedCh:
			open(t.status)
		case <-t.mToggle.ClickedCh:
			t.toggle()
		case <-mLog.ClickedCh:
			open(filepath.Join(t.dir, logName))
		case <-mConfig.ClickedCh:
			open(t.dir)
		case <-mQuit.ClickedCh:
			systray.Quit()
			return
		}
	}
}

// start runs the daemon in this process.
func (t *tray) start() {
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	t.done = make(chan struct{})

	go func() {
		defer close(t.done)
		d, err := daemon.New(t.cfg)
		if err != nil {
			log.Printf("startup error: %v", err)
			systray.SetTooltip("Gnodi AI Node - failed to start, see the log")
			return
		}
		if err := d.Run(ctx); err != nil && ctx.Err() == nil {
			log.Printf("daemon exited with error: %v", err)
			systray.SetTooltip("Gnodi AI Node - stopped, see the log")
		}
	}()
}

// onExit drains before closing, so a job in flight is finished rather than
// dropped on the gateway's floor.
func (t *tray) onExit() {
	if t.cancel == nil {
		return
	}
	log.Printf("quit requested, draining")
	t.cancel()
	select {
	case <-t.done:
	case <-time.After(20 * time.Second):
		log.Printf("still draining after 20s, closing anyway")
	}
}

// toggle flips serving through the daemon's own loopback route, rather than
// calling into it directly: one code path for the tray and the dashboard means
// they cannot disagree about what "stopped" means.
func (t *tray) toggle() {
	serving := t.serving()
	body := strings.NewReader(fmt.Sprintf(`{"on":%t}`, !serving))
	resp, err := http.Post(t.status+"/serving", "application/json", body)
	if err != nil {
		log.Printf("could not change serving state: %v", err)
		return
	}
	defer resp.Body.Close()
	t.refresh()
}

func (t *tray) serving() bool {
	s, err := t.fetchStatus()
	return err == nil && s.Serving
}

type statusView struct {
	Serving  bool     `json:"serving"`
	Models   []string `json:"models"`
	InFlight int      `json:"inFlight"`
	VramGb   int      `json:"vramGb"`
}

func (t *tray) fetchStatus() (statusView, error) {
	var s statusView
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(t.status + "/status")
	if err != nil {
		return s, err
	}
	defer resp.Body.Close()
	err = json.NewDecoder(resp.Body).Decode(&s)
	return s, err
}

// poll keeps the menu honest. The dashboard, the daemon and this icon are three
// views of one state, and an icon that says "serving" while the node is paused
// is worse than an icon that says nothing.
func (t *tray) poll() {
	for {
		t.refresh()
		time.Sleep(pollWait)
	}
}

func (t *tray) refresh() {
	s, err := t.fetchStatus()
	if err != nil {
		systray.SetTooltip("Gnodi AI Node - starting, or not responding")
		return
	}
	if s.Serving {
		t.mToggle.SetTitle("Stop serving")
		systray.SetTooltip(fmt.Sprintf("Gnodi AI Node - serving %d model(s), %d in flight",
			len(s.Models), s.InFlight))
	} else {
		t.mToggle.SetTitle("Start serving")
		systray.SetTooltip("Gnodi AI Node - paused, the GPU is yours")
	}
}

// --- plumbing --------------------------------------------------------------

func exeDir() string {
	p, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(p)
}

// startLogging sends the standard logger to a file, because -H=windowsgui
// leaves nowhere else for it to go.
func startLogging(dir string) func() {
	f, err := os.OpenFile(filepath.Join(dir, logName),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return func() {}
	}
	log.SetOutput(f)
	log.Printf("---- started %s ----", time.Now().Format(time.RFC3339))
	return func() { _ = f.Close() }
}

// open hands a URL, file or folder to Windows to deal with.
func open(target string) {
	if target == "" {
		return
	}
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	if err := cmd.Start(); err != nil {
		log.Printf("could not open %s: %v", target, err)
	}
}
