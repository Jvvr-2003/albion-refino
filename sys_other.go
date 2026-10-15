//go:build !windows

package main

// Versão para Linux/macOS. O app é feito para Windows; aqui ele roda para
// testes, com um Data Client simulado apontado por ALBION_FAKE_CLIENT.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func platformName() string { return "test" }

func findClientExe(dataDir string) string {
	if p := os.Getenv("ALBION_FAKE_CLIENT"); p != "" {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

func queryAutostartTask() taskInfo { return taskInfo{Known: true, Exists: false} }

func npcapState() string { return "na" }

func listClientPIDs() ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, nil
	}
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil || len(raw) == 0 {
			continue
		}
		argv0 := strings.SplitN(string(raw), "\x00", 2)[0]
		if filepath.Base(argv0) == "albiondata-client" {
			if st, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat")); err == nil && strings.Contains(string(st), ") Z ") {
				continue // processo zumbi
			}
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

func killUnelevatedClients() {
	pids, _ := listClientPIDs()
	for _, pid := range pids {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
	}
}

func logCandidates(dataDir, exe string) []string {
	return []string{filepath.Join(dataDir, "dataclient", "albiondata-client.log")}
}

func startClientProcess(exe, dir string, args []string) (*exec.Cmd, error) {
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

func runElevatedScript(path string, timeout time.Duration) (int, error) {
	return -1, ErrNotWindows
}

func startClientElevated(exe, dir string, args []string) error { return ErrNotWindows }

func isElevated() bool { return os.Geteuid() == 0 }

func openAppWindow(url, profileDir string) error {
	fmt.Fprintln(os.Stderr, "Abra no navegador:", url)
	return nil
}

func openFileInEditor(path string) error { return ErrNotWindows }

func openFolder(path string) error { return ErrNotWindows }

func showErrorBox(title, msg string) { fmt.Fprintln(os.Stderr, title+": "+msg) }

func defaultDataDir() string {
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "albionrefino")
	}
	return "albionrefino-data"
}
