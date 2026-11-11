// Package paths is the single source of every on-disk location. The installer,
// uninstall, service unit and docs all read them from `multica-intake paths --json`.
package paths

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// Paths are the locations the tool owns.
type Paths struct {
	ConfigDir  string `json:"config_dir"`
	ConfigFile string `json:"config_file"`
	StateDir   string `json:"state_dir"`
	Binary     string `json:"binary"`
	Marker     string `json:"marker"`     // .multica-intake-managed next to the binary (installer ownership)
	UnitFile   string `json:"unit_file"`  // systemd user unit (Linux only; empty elsewhere)
	TokenFile  string `json:"token_file"` // optional PAT file for `mcp` over stdio
}

// Resolve applies the override env vars (one meaning each), then OS conventions.
//
//	MULTICA_INTAKE_CONFIG_DIR  config dir   (default: $XDG_CONFIG_HOME/multica-intake, ~/Library/Application Support/multica-intake)
//	MULTICA_INTAKE_STATE_DIR   state dir    (default: $XDG_STATE_HOME/multica-intake, same as config on macOS)
func Resolve() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, errors.New("cannot find the home directory: set HOME")
	}
	cfg := os.Getenv("MULTICA_INTAKE_CONFIG_DIR")
	if cfg == "" {
		switch runtime.GOOS {
		case "darwin":
			cfg = filepath.Join(home, "Library", "Application Support", "multica-intake")
		default:
			base := os.Getenv("XDG_CONFIG_HOME")
			if base == "" {
				base = filepath.Join(home, ".config")
			}
			cfg = filepath.Join(base, "multica-intake")
		}
	}
	state := os.Getenv("MULTICA_INTAKE_STATE_DIR")
	if state == "" {
		switch runtime.GOOS {
		case "darwin":
			state = cfg
		default:
			base := os.Getenv("XDG_STATE_HOME")
			if base == "" {
				base = filepath.Join(home, ".local", "state")
			}
			state = filepath.Join(base, "multica-intake")
		}
	}
	bin, _ := os.Executable()
	if r, err := filepath.EvalSymlinks(bin); err == nil {
		bin = r
	}
	p := Paths{
		ConfigDir:  cfg,
		ConfigFile: filepath.Join(cfg, "config.yaml"),
		StateDir:   state,
		Binary:     bin,
		Marker:     filepath.Join(filepath.Dir(bin), ".multica-intake-managed"),
		TokenFile:  filepath.Join(cfg, "token"),
	}
	if runtime.GOOS == "linux" {
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(home, ".config")
		}
		p.UnitFile = filepath.Join(base, "systemd", "user", "multica-intake.service")
	}
	return p, nil
}
