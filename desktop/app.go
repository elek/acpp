package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"gopkg.in/yaml.v3"
)

// desktopConfig mirrors the subset of acpp config.yaml we care about.
type desktopConfig struct {
	Desktop struct {
		Zoom float64 `yaml:"zoom,omitempty"`
	} `yaml:"desktop,omitempty"`
}

type App struct {
	ctx context.Context
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

func (a *App) domReady(ctx context.Context) {
	initialZoom := loadZoom()

	// Inject zoom manager before redirect so it's ready on the target page too.
	// domReady fires on every navigation (including after redirect), so this
	// script will be re-injected on the localhost:6060 page as well.
	runtime.WindowExecJS(a.ctx, fmt.Sprintf(`
		(function() {
			// Seed localStorage from config on very first load if not set yet.
			var configZoom = %g;
			if (configZoom !== 1 && !localStorage.getItem('acpp-zoom')) {
				localStorage.setItem('acpp-zoom', String(configZoom));
			}

			// Apply saved zoom.
			var zoom = parseFloat(localStorage.getItem('acpp-zoom')) || 1;
			document.body.style.zoom = String(zoom);

			// Keyboard shortcuts: Ctrl+Shift++ / Ctrl+Shift+- / Ctrl+Shift+0
			if (!window.__acppZoomRegistered) {
				window.__acppZoomRegistered = true;
				document.addEventListener('keydown', function(e) {
					if (!e.ctrlKey || !e.shiftKey) return;
					var zoom = parseFloat(localStorage.getItem('acpp-zoom')) || 1;
					if (e.key === '+' || e.key === '=') {
						e.preventDefault();
						zoom = Math.min(3, Math.round((zoom + 0.1) * 10) / 10);
					} else if (e.key === '-' || e.key === '_') {
						e.preventDefault();
						zoom = Math.max(0.5, Math.round((zoom - 0.1) * 10) / 10);
					} else if (e.key === '0') {
						e.preventDefault();
						zoom = 1;
					} else {
						return;
					}
					localStorage.setItem('acpp-zoom', String(zoom));
					document.body.style.zoom = String(zoom);
				});
			}

			// Redirect to web UI if not already there.
			if (!window.location.href.startsWith("http://localhost:")) {
				window.location.replace("http://localhost:6060");
			}
		})();
	`, initialZoom))
}

// configPath returns the acpp config file path.
func configPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "acpp", "config.yaml")
}

// loadZoom reads the zoom level from the config file.
func loadZoom() float64 {
	path := configPath()
	if path == "" {
		return 1.0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 1.0
	}
	var cfg desktopConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return 1.0
	}
	if cfg.Desktop.Zoom == 0 {
		return 1.0
	}
	return cfg.Desktop.Zoom
}
