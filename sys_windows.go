//go:build windows

package main

import (
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

var (
	modShell32   = syscall.NewLazyDLL("shell32.dll")
	modUser32    = syscall.NewLazyDLL("user32.dll")
	modOle32     = syscall.NewLazyDLL("ole32.dll")
	procShellEx  = modShell32.NewProc("ShellExecuteExW")
	procMsgBox   = modUser32.NewProc("MessageBoxW")
	procCoInit   = modOle32.NewProc("CoInitializeEx")
	procCoUninit = modOle32.NewProc("CoUninitialize")
)

const (
	createNoWindow        = 0x08000000
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
	seeMaskFlagNoUI       = 0x00000400
	swHide                = 0
	swShowNormal          = 1
	errorCancelled        = 1223
	keyWow6464Key         = 0x0100
	waitTimeout           = 0x00000102
)

type shellExecuteInfo struct {
	cbSize         uint32
	fMask          uint32
	hwnd           uintptr
	lpVerb         *uint16
	lpFile         *uint16
	lpParameters   *uint16
	lpDirectory    *uint16
	nShow          int32
	hInstApp       uintptr
	lpIDList       uintptr
	lpClass        *uint16
	hkeyClass      uintptr
	dwHotKey       uint32
	hIconOrMonitor uintptr
	hProcess       syscall.Handle
}

func platformName() string { return "windows" }

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// ---------- registro ----------

func regOpen(path string) (syscall.Handle, error) {
	var h syscall.Handle
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	err = syscall.RegOpenKeyEx(syscall.HKEY_LOCAL_MACHINE, p, 0, syscall.KEY_READ|keyWow6464Key, &h)
	return h, err
}

func regValue(path, name string) (uint32, []byte, error) {
	h, err := regOpen(path)
	if err != nil {
		return 0, nil, err
	}
	defer syscall.RegCloseKey(h)
	var namePtr *uint16
	if name != "" {
		namePtr, _ = syscall.UTF16PtrFromString(name)
	}
	var typ, size uint32
	if err := syscall.RegQueryValueEx(h, namePtr, nil, &typ, nil, &size); err != nil {
		return 0, nil, err
	}
	if size == 0 {
		return typ, nil, nil
	}
	buf := make([]byte, size)
	if err := syscall.RegQueryValueEx(h, namePtr, nil, &typ, &buf[0], &size); err != nil {
		return 0, nil, err
	}
	return typ, buf[:size], nil
}

func regString(path, name string) string {
	typ, buf, err := regValue(path, name)
	if err != nil || (typ != syscall.REG_SZ && typ != syscall.REG_EXPAND_SZ) || len(buf) < 2 {
		return ""
	}
	u := make([]uint16, len(buf)/2)
	for i := range u {
		u[i] = uint16(buf[2*i]) | uint16(buf[2*i+1])<<8
	}
	return strings.TrimSpace(syscall.UTF16ToString(u))
}

func regKeyExists(path string) bool {
	h, err := regOpen(path)
	if err != nil {
		return false
	}
	syscall.RegCloseKey(h)
	return true
}

// ---------- Data Client ----------

func findClientExe(dataDir string) string {
	var cands []string
	if dir := regString(`SOFTWARE\Albion Data Client`, ""); dir != "" {
		cands = append(cands, filepath.Join(dir, "albiondata-client.exe"))
	}
	if dir := regString(`SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\Albion Data Client`, "InstallLocation"); dir != "" {
		cands = append(cands, filepath.Join(dir, "albiondata-client.exe"))
	}
	for _, env := range []string{"ProgramW6432", "ProgramFiles", "ProgramFiles(x86)"} {
		if v := os.Getenv(env); v != "" {
			cands = append(cands, filepath.Join(v, "Albion Data Client", "albiondata-client.exe"))
		}
	}
	cands = append(cands, filepath.Join(dataDir, "dataclient", "albiondata-client.exe"))
	for _, c := range cands {
		if fileExists(c) {
			return c
		}
	}
	return ""
}

func npcapState() string {
	for _, path := range []string{`SOFTWARE\Npcap`, `SOFTWARE\WOW6432Node\Npcap`} {
		if !regKeyExists(path) {
			continue
		}
		typ, buf, err := regValue(path, "AdminOnly")
		if err == nil && typ == syscall.REG_DWORD && len(buf) >= 4 {
			if buf[0]|buf[1]|buf[2]|buf[3] != 0 {
				return "adminonly"
			}
		}
		return "ok"
	}
	for _, path := range []string{`SOFTWARE\WinPcap`, `SOFTWARE\WOW6432Node\WinPcap`} {
		if regKeyExists(path) {
			return "winpcap"
		}
	}
	return "missing"
}

func utilCmd(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return c
}

func utilOutput(name string, args ...string) ([]byte, int, error) {
	out, err := utilCmd(name, args...).CombinedOutput()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return out, ee.ExitCode(), nil
		}
		return out, -1, err
	}
	return out, 0, nil
}

func decodeMaybeUTF16(b []byte) string {
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		b = b[2:]
	} else {
		zeros := 0
		for i := 1; i < len(b) && i < 400; i += 2 {
			if b[i] == 0 {
				zeros++
			}
		}
		if zeros < 20 {
			return string(b)
		}
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return string(utf16.Decode(u))
}

var (
	reTaskCommand = regexp.MustCompile(`(?s)<Command>(.*?)</Command>`)
	reTaskArgs    = regexp.MustCompile(`(?s)<Arguments>(.*?)</Arguments>`)
)

func queryAutostartTask() taskInfo {
	out, code, err := utilOutput("schtasks", "/Query", "/TN", taskName, "/XML")
	if err != nil {
		return taskInfo{}
	}
	if code != 0 {
		low := strings.ToLower(string(out))
		if strings.Contains(low, "encontr") || strings.Contains(low, "cannot find") || strings.Contains(low, "does not exist") {
			return taskInfo{Known: true, Exists: false}
		}
		return taskInfo{}
	}
	x := decodeMaybeUTF16(out)
	t := taskInfo{Known: true, Exists: true}
	if m := reTaskCommand.FindStringSubmatch(x); m != nil {
		t.Command = html.UnescapeString(strings.TrimSpace(m[1]))
	}
	if m := reTaskArgs.FindStringSubmatch(x); m != nil {
		t.Args = html.UnescapeString(strings.TrimSpace(m[1]))
	}
	if t.Command == "" {
		// Não deu para ler o XML: melhor não tirar conclusão sobre a ligação.
		return taskInfo{}
	}
	return t
}

func listClientPIDs() ([]int, error) {
	out, _, err := utilOutput("tasklist", "/FI", "IMAGENAME eq albiondata-client.exe", "/FO", "CSV", "/NH")
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToLower(line), `"albiondata-client.exe"`) {
			continue
		}
		parts := strings.Split(line, `","`)
		if len(parts) < 2 {
			continue
		}
		if pid, err := strconv.Atoi(strings.Trim(parts[1], `" `)); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

func killUnelevatedClients() {
	_, _, _ = utilOutput("taskkill", "/F", "/IM", "albiondata-client.exe")
}

func logCandidates(dataDir, exe string) []string {
	var c []string
	if root := os.Getenv("SystemRoot"); root != "" {
		c = append(c, filepath.Join(root, "System32", "albiondata-client.log"))
	} else {
		c = append(c, `C:\Windows\System32\albiondata-client.log`)
	}
	if exe != "" {
		c = append(c, filepath.Join(filepath.Dir(exe), "albiondata-client.log"))
	}
	c = append(c, filepath.Join(dataDir, "dataclient", "albiondata-client.log"))
	return c
}

func startClientProcess(exe, dir string, args []string) (*exec.Cmd, error) {
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	// Console escondido: o Data Client é um programa de console e, sem isso,
	// abriria uma janela preta junto.
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// ---------- permissão de administrador ----------

func shellExecuteRunAs(file, params, dir string, show int32, wait time.Duration) (int, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if r, _, _ := procCoInit.Call(0, 0x2|0x4); r == 0 || r == 1 {
		defer procCoUninit.Call()
	}

	verb, _ := syscall.UTF16PtrFromString("runas")
	f, err := syscall.UTF16PtrFromString(file)
	if err != nil {
		return -1, err
	}
	p, err := syscall.UTF16PtrFromString(params)
	if err != nil {
		return -1, err
	}
	var d *uint16
	if dir != "" {
		d, _ = syscall.UTF16PtrFromString(dir)
	}
	info := shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess | seeMaskNoAsync | seeMaskFlagNoUI,
		lpVerb:       verb,
		lpFile:       f,
		lpParameters: p,
		lpDirectory:  d,
		nShow:        show,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))
	r, _, callErr := procShellEx.Call(uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		if errno, ok := callErr.(syscall.Errno); ok && errno == errorCancelled {
			return -1, ErrCancelled
		}
		return -1, fmt.Errorf("não consegui pedir permissão de administrador: %v", callErr)
	}
	if info.hProcess == 0 {
		return 0, nil
	}
	defer syscall.CloseHandle(info.hProcess)
	if wait <= 0 {
		return 0, nil
	}
	ev, err := syscall.WaitForSingleObject(info.hProcess, uint32(wait.Milliseconds()))
	if err != nil {
		return -1, err
	}
	if ev == waitTimeout {
		return -1, errors.New("o script de administrador demorou demais para terminar")
	}
	var code uint32
	if err := syscall.GetExitCodeProcess(info.hProcess, &code); err != nil {
		return -1, err
	}
	return int(code), nil
}

func runElevatedScript(path string, timeout time.Duration) (int, error) {
	cmdExe := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	if !fileExists(cmdExe) {
		cmdExe = "cmd.exe"
	}
	return shellExecuteRunAs(cmdExe, `/d /c ""`+path+`""`, filepath.Dir(path), swHide, timeout)
}

func startClientElevated(exe, dir string, args []string) error {
	_ = os.MkdirAll(dir, 0o755)
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = syscall.EscapeArg(a)
	}
	_, err := shellExecuteRunAs(exe, strings.Join(quoted, " "), dir, swHide, 0)
	return err
}

func isElevated() bool {
	proc, err := syscall.GetCurrentProcess()
	if err != nil {
		return false
	}
	var tok syscall.Token
	if err := syscall.OpenProcessToken(proc, syscall.TOKEN_QUERY, &tok); err != nil {
		return false
	}
	defer tok.Close()
	var elevation uint32
	var n uint32
	if err := syscall.GetTokenInformation(tok, 20 /* TokenElevation */, (*byte)(unsafe.Pointer(&elevation)), 4, &n); err != nil {
		return false
	}
	return elevation != 0
}

// ---------- janela do app ----------

func browserCandidates() []string {
	pf86 := os.Getenv("ProgramFiles(x86)")
	pf := os.Getenv("ProgramFiles")
	local := os.Getenv("LOCALAPPDATA")
	return []string{
		filepath.Join(pf86, `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(pf, `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(local, `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(pf, `Google\Chrome\Application\chrome.exe`),
		filepath.Join(pf86, `Google\Chrome\Application\chrome.exe`),
		filepath.Join(local, `Google\Chrome\Application\chrome.exe`),
		filepath.Join(pf, `BraveSoftware\Brave-Browser\Application\brave.exe`),
		filepath.Join(local, `BraveSoftware\Brave-Browser\Application\brave.exe`),
	}
}

// openAppWindow abre o app numa janela própria (modo aplicativo do Edge,
// sem barra de endereço), com um perfil separado do navegador da pessoa.
func openAppWindow(url, profileDir string) error {
	first := false
	if _, err := os.Stat(profileDir); err != nil {
		first = true
	}
	args := []string{
		"--app=" + url,
		"--user-data-dir=" + profileDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--hide-crash-restore-bubble",
		"--disable-features=Translate,msEdgeSidebarV2",
	}
	if first {
		args = append(args, "--window-size=1380,900")
	}
	for _, exe := range browserCandidates() {
		if !fileExists(exe) {
			continue
		}
		cmd := exec.Command(exe, args...)
		if err := cmd.Start(); err == nil {
			go cmd.Wait()
			return nil
		}
	}
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

func openFileInEditor(path string) error {
	cmd := exec.Command("notepad.exe", path)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

func openFolder(path string) error {
	cmd := exec.Command("explorer.exe", path)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

func showErrorBox(title, msg string) {
	t, _ := syscall.UTF16PtrFromString(title)
	m, _ := syscall.UTF16PtrFromString(msg)
	procMsgBox.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), 0x10)
}

func defaultDataDir() string {
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		return filepath.Join(v, "AlbionRefino")
	}
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "AlbionRefino")
	}
	return "AlbionRefino"
}
