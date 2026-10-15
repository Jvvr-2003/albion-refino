package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const taskName = "Albion Data Client"

// ClientStatus é o que a aba Captura mostra sobre o Albion Data Client.
type ClientStatus struct {
	Platform       string `json:"platform"`
	Exe            string `json:"exe"`
	Installed      bool   `json:"installed"`
	Version        string `json:"version"`
	TaskKnown      bool   `json:"taskKnown"`
	TaskExists     bool   `json:"taskExists"`
	TaskArgs       string `json:"taskArgs"`
	Linked         bool   `json:"linked"`
	LinkedAt       int64  `json:"linkedAt"`
	Running        bool   `json:"running"`
	PIDs           []int  `json:"pids"`
	Managed        bool   `json:"managed"`
	AutoStart      bool   `json:"autoStart"`
	Elevated       bool   `json:"elevated"`
	Npcap          string `json:"npcap"`
	Location       string `json:"location"`
	LocationRaw    string `json:"locationRaw"`
	LocationAt     int64  `json:"locationAt"`
	Character      string `json:"character"`
	ServerHint     string `json:"serverHint"`
	LastIngest     int64  `json:"lastIngest"`
	IngestOrders   int64  `json:"ingestOrders"`
	IngestCity     string `json:"ingestCity"`
	PublicSends    int64  `json:"publicSends"`
	LastPublicSend int64  `json:"lastPublicSend"`
	Encrypted      int64  `json:"encrypted"`
	NeedZone       bool   `json:"needZone"`
	CaptureErr     string `json:"captureErr"`
	LogPath        string `json:"logPath"`
	Busy           string `json:"busy"`
	Message        string `json:"message"`
	MessageKind    string `json:"messageKind"`
	MessageAt      int64  `json:"messageAt"`
	IngestURL      string `json:"ingestURL"`
	ManualCmd      string `json:"manualCmd"`
}

// Prefs são as escolhas do app sobre o Data Client, salvas em prefs.json.
type Prefs struct {
	AutoStart  bool   `json:"autoStart"`
	LinkedAt   int64  `json:"linkedAt"`
	LinkedArgs string `json:"linkedArgs"`
}

type taskInfo struct {
	Known   bool
	Exists  bool
	Command string
	Args    string
}

// ErrCancelled indica que a pessoa recusou a permissão de administrador.
var ErrCancelled = errors.New("permissão de administrador recusada")

// ErrNotWindows indica uma ação que só existe no Windows.
var ErrNotWindows = errors.New("essa ação só funciona no Windows")

type DataClient struct {
	app       *App
	ingestURL string
	prefsPath string

	mu    sync.Mutex
	st    ClientStatus
	prefs Prefs

	logPath   string
	logOffset int64
	logLines  []string
	newLines  []string
	logReset  bool

	managed     *exec.Cmd
	managedPID  int
	everManaged bool

	actionMu      sync.Mutex
	startedAt     time.Time
	autoTried     bool
	lastPublished []byte
}

func NewDataClient(app *App) *DataClient {
	dc := &DataClient{
		app:       app,
		ingestURL: fmt.Sprintf("http://127.0.0.1:%d/ingest", app.port),
		prefsPath: filepath.Join(app.dataDir, "prefs.json"),
		prefs:     Prefs{AutoStart: true},
		startedAt: time.Now(),
	}
	if raw, err := os.ReadFile(dc.prefsPath); err == nil {
		_ = json.Unmarshal(raw, &dc.prefs)
	}
	dc.st.Platform = platformName()
	dc.st.IngestURL = dc.ingestURL
	dc.st.Elevated = isElevated()
	return dc
}

func (dc *DataClient) savePrefsLocked() {
	raw, _ := json.MarshalIndent(dc.prefs, "", "  ")
	if err := writeFileAtomic(dc.prefsPath, raw); err != nil {
		dc.app.logf("prefs: %v", err)
	}
}

// Status devolve uma cópia do estado atual.
func (dc *DataClient) Status() ClientStatus {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	return dc.statusLocked()
}

func (dc *DataClient) statusLocked() ClientStatus {
	s := dc.st
	s.PIDs = append([]int(nil), dc.st.PIDs...)
	s.AutoStart = dc.prefs.AutoStart
	s.LinkedAt = dc.prefs.LinkedAt
	s.Managed = dc.managed != nil
	if s.TaskKnown && s.TaskExists {
		s.Linked = strings.Contains(s.TaskArgs, dc.ingestURL)
	} else {
		s.Linked = dc.prefs.LinkedAt > 0
	}
	if s.Managed {
		s.Linked = true
	}
	if s.LastIngest > 0 {
		s.Linked = true
	}
	exe := s.Exe
	if exe == "" {
		exe = `C:\Program Files\Albion Data Client\albiondata-client.exe`
	}
	s.ManualCmd = fmt.Sprintf(`schtasks /Create /F /SC ONLOGON /RL HIGHEST /TN "%s" /TR "\"%s\" -minimize -p %s"`, taskName, exe, dc.ingestURL)
	return s
}

// LogLines devolve as últimas linhas do log do Data Client.
func (dc *DataClient) LogLines() []string {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	return append([]string(nil), dc.logLines...)
}

func (dc *DataClient) setMessage(kind, msg string) {
	dc.mu.Lock()
	dc.st.Message = msg
	dc.st.MessageKind = kind
	dc.st.MessageAt = time.Now().UnixMilli()
	dc.mu.Unlock()
	dc.publish(true)
}

// NoteIngest registra que chegaram ordens do Data Client.
func (dc *DataClient) NoteIngest(orders int, city string) {
	dc.mu.Lock()
	dc.st.LastIngest = time.Now().UnixMilli()
	dc.st.IngestOrders += int64(orders)
	if city != "" {
		dc.st.IngestCity = city
	}
	dc.mu.Unlock()
	dc.publish(false)
}

// publish manda o status para a tela quando ele mudou.
func (dc *DataClient) publish(force bool) {
	dc.mu.Lock()
	s := dc.statusLocked()
	raw, err := json.Marshal(s)
	if err != nil {
		dc.mu.Unlock()
		return
	}
	same := bytes.Equal(raw, dc.lastPublished)
	dc.lastPublished = raw
	dc.mu.Unlock()
	if same && !force {
		return
	}
	dc.app.hub.BroadcastRaw("status", raw)
}

// Run mantém o status atualizado: processo, tarefa, Npcap e log.
func (dc *DataClient) Run(stop <-chan struct{}) {
	dc.detectStatic()
	dc.refreshProcesses()
	dc.pickLogFile()
	dc.tailLog()
	dc.flushLog()
	dc.publish(true)

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	n := 0
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
		}
		n++
		dc.tailLog()
		dc.flushLog()
		if n%4 == 0 {
			dc.refreshProcesses()
			dc.maybeAutoStart()
		}
		if n%15 == 0 {
			dc.pickLogFile()
		}
		if n%90 == 0 {
			dc.detectStatic()
		}
		dc.publish(false)
	}
}

func (dc *DataClient) detectStatic() {
	exe := findClientExe(dc.app.dataDir)
	task := queryAutostartTask()
	npcap := npcapState()
	dc.mu.Lock()
	dc.st.Exe = exe
	dc.st.Installed = exe != ""
	dc.st.TaskKnown = task.Known
	dc.st.TaskExists = task.Exists
	dc.st.TaskArgs = task.Args
	if task.Known && task.Exists && !strings.Contains(task.Args, dc.ingestURL) && dc.prefs.LinkedAt > 0 {
		// A tarefa foi recriada sem a ligação (por exemplo, reinstalação).
		dc.prefs.LinkedAt = 0
		dc.prefs.LinkedArgs = ""
		dc.savePrefsLocked()
	}
	if npcap != "" {
		dc.st.Npcap = npcap
	}
	dc.mu.Unlock()
}

func (dc *DataClient) refreshProcesses() {
	pids, err := listClientPIDs()
	dc.mu.Lock()
	defer dc.mu.Unlock()
	if err != nil {
		return
	}
	dc.st.PIDs = pids
	dc.st.Running = len(pids) > 0
	// Duas cópias capturando ao mesmo tempo: fecha a que o app abriu.
	if dc.managed != nil && len(pids) > 1 {
		dc.app.logf("data client: outra cópia já está rodando, fechando a do app (pid %d)", dc.managedPID)
		_ = dc.managed.Process.Kill()
	}
}

func (dc *DataClient) maybeAutoStart() {
	dc.mu.Lock()
	ok := !dc.autoTried && dc.prefs.AutoStart && !dc.st.Running && dc.st.Exe != "" && dc.managed == nil &&
		time.Since(dc.startedAt) > 6*time.Second
	if ok {
		dc.autoTried = true
	}
	npcap := dc.st.Npcap
	elevated := dc.st.Elevated
	dc.mu.Unlock()
	if !ok {
		return
	}
	if npcap == "adminonly" && !elevated {
		dc.setMessage("warn", L("O Data Client está fechado. Como o seu Npcap só permite captura para administradores, clique em \"Abrir Data Client\" e aceite a permissão do Windows.", "The Data Client is closed. Since your Npcap only allows capture for administrators, click \"Open Data Client\" and accept the Windows permission."))
		return
	}
	if err := dc.startManaged(); err != nil {
		dc.setMessage("err", L("Não consegui abrir o Data Client: ", "Could not open the Data Client: ")+err.Error())
		return
	}
	dc.setMessage("ok", L("Abri o Data Client junto com o app. Os preços do mercado vão chegar aqui enquanto o app estiver aberto.", "Opened the Data Client with the app. Market prices will arrive here while the app is open."))
}

func (dc *DataClient) clientDir() string {
	return filepath.Join(dc.app.dataDir, "dataclient")
}

// startManaged abre o Data Client oficial com -p apontando para o app.
func (dc *DataClient) startManaged() error {
	dc.mu.Lock()
	exe := dc.st.Exe
	running := dc.managed != nil
	dc.mu.Unlock()
	if running {
		return nil
	}
	if exe == "" {
		return errors.New(L("o Albion Data Client não foi encontrado neste PC", "the Albion Data Client was not found on this PC"))
	}
	dir := dc.clientDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	cmd, err := startClientProcess(exe, dir, []string{"-p", dc.ingestURL})
	if err != nil {
		return err
	}
	dc.mu.Lock()
	dc.managed = cmd
	dc.managedPID = cmd.Process.Pid
	dc.everManaged = true
	dc.mu.Unlock()
	dc.app.logf("data client: aberto pelo app (pid %d)", cmd.Process.Pid)
	started := time.Now()
	go func() {
		err := cmd.Wait()
		dc.mu.Lock()
		if dc.managed == cmd {
			dc.managed = nil
			dc.managedPID = 0
		}
		dc.mu.Unlock()
		dc.app.logf("data client: processo do app terminou (%v)", err)
		if time.Since(started) < 15*time.Second && err != nil {
			dc.setMessage("err", fmt.Sprintf("O Data Client fechou logo depois de abrir (%v). Veja o log abaixo.", err))
		}
		dc.refreshProcesses()
		dc.publish(true)
	}()
	dc.pickLogFile()
	dc.publish(true)
	return nil
}

// StopManaged fecha o Data Client que o app abriu.
func (dc *DataClient) StopManaged() {
	dc.mu.Lock()
	cmd := dc.managed
	dc.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	dc.app.logf("data client: fechando a cópia aberta pelo app (pid %d)", cmd.Process.Pid)
	_ = cmd.Process.Kill()
}

// Shutdown roda ao fechar o app. Se o app abriu o Data Client, fecha as
// cópias sem permissão de administrador (a do início automático, que roda
// como administrador, continua aberta).
func (dc *DataClient) Shutdown() {
	dc.StopManaged()
	dc.mu.Lock()
	ever := dc.everManaged
	dc.mu.Unlock()
	if ever {
		killUnelevatedClients()
	}
}

// Action executa um botão da aba Captura.
func (dc *DataClient) Action(name string) {
	if !dc.actionMu.TryLock() {
		dc.setMessage("warn", L("Espere a ação anterior terminar.", "Wait for the previous action to finish."))
		return
	}
	go func() {
		defer dc.actionMu.Unlock()
		dc.runAction(name)
		dc.mu.Lock()
		dc.st.Busy = ""
		dc.mu.Unlock()
		dc.detectStatic()
		dc.refreshProcesses()
		dc.pickLogFile()
		dc.publish(true)
	}()
}

func (dc *DataClient) setBusy(label string) {
	dc.mu.Lock()
	dc.st.Busy = label
	dc.mu.Unlock()
	dc.publish(true)
}

func (dc *DataClient) runAction(name string) {
	dc.detectStatic()
	dc.refreshProcesses()
	st := dc.Status()
	switch name {
	case "link":
		if st.Exe == "" {
			dc.setMessage("err", L("Não encontrei o Albion Data Client instalado. Instale pelo site do Albion Online Data Project e tente de novo.", "Could not find the Albion Data Client installed. Install it from the Albion Online Data Project site and try again."))
			return
		}
		dc.setBusy(L("Esperando a permissão do Windows…", "Waiting for Windows permission…"))
		args := "-minimize -p " + dc.ingestURL
		res, err := dc.runTaskScript(st.Exe, args, true)
		if err != nil {
			dc.reportScriptError(err, res)
			return
		}
		dc.mu.Lock()
		dc.prefs.LinkedAt = time.Now().UnixMilli()
		dc.prefs.LinkedArgs = args
		dc.savePrefsLocked()
		dc.mu.Unlock()
		dc.setMessage("ok", L("Pronto. O Data Client foi reiniciado e agora manda os preços para o app. Navegue no mercado do jogo para testar.", "Done. The Data Client was restarted and now sends prices to the app. Browse the in-game market to test."))
	case "unlink":
		if st.Exe == "" {
			dc.setMessage("err", L("Não encontrei o Albion Data Client instalado.", "Could not find the Albion Data Client installed."))
			return
		}
		dc.setBusy(L("Esperando a permissão do Windows…", "Waiting for Windows permission…"))
		res, err := dc.runTaskScript(st.Exe, "-minimize", true)
		if err != nil {
			dc.reportScriptError(err, res)
			return
		}
		dc.mu.Lock()
		dc.prefs.LinkedAt = 0
		dc.prefs.LinkedArgs = ""
		dc.savePrefsLocked()
		dc.mu.Unlock()
		dc.setMessage("ok", L("O início automático do Data Client voltou ao padrão do instalador, sem mandar preços para o app.", "The Data Client autostart is back to the installer default, without sending prices to the app."))
	case "start":
		if st.Running {
			dc.setMessage("ok", L("O Data Client já está rodando.", "The Data Client is already running."))
			return
		}
		if st.Exe == "" {
			dc.setMessage("err", L("Não encontrei o Albion Data Client instalado.", "Could not find the Albion Data Client installed."))
			return
		}
		if st.Npcap == "adminonly" && !st.Elevated {
			dc.setBusy(L("Esperando a permissão do Windows…", "Waiting for Windows permission…"))
			if err := startClientElevated(st.Exe, dc.clientDir(), []string{"-p", dc.ingestURL}); err != nil {
				if errors.Is(err, ErrCancelled) {
					dc.setMessage("warn", L("Você recusou a permissão do Windows. O Data Client não foi aberto.", "You declined the Windows permission. The Data Client was not opened."))
				} else {
					dc.setMessage("err", L("Não consegui abrir o Data Client como administrador: ", "Could not open the Data Client as administrator: ")+err.Error())
				}
				return
			}
			dc.setMessage("ok", L("Data Client aberto como administrador, mandando preços para o app.", "Data Client opened as administrator, sending prices to the app."))
			return
		}
		if err := dc.startManaged(); err != nil {
			dc.setMessage("err", L("Não consegui abrir o Data Client: ", "Could not open the Data Client: ")+err.Error())
			return
		}
		dc.setMessage("ok", L("Data Client aberto. Os preços do mercado vão chegar aqui enquanto o app estiver aberto.", "Data Client opened. Market prices will arrive here while the app is open."))
	case "restart":
		if dc.Status().Managed {
			dc.StopManaged()
			time.Sleep(1500 * time.Millisecond)
			if err := dc.startManaged(); err != nil {
				dc.setMessage("err", L("Não consegui reabrir o Data Client: ", "Could not reopen the Data Client: ")+err.Error())
				return
			}
			dc.setMessage("ok", L("Data Client reaberto.", "Data Client reopened."))
			return
		}
		if st.Exe == "" {
			dc.setMessage("err", L("Não encontrei o Albion Data Client instalado.", "Could not find the Albion Data Client installed."))
			return
		}
		dc.setBusy(L("Esperando a permissão do Windows…", "Waiting for Windows permission…"))
		res, err := dc.runRestartScript(st.Exe, st.TaskExists || st.LinkedAt > 0)
		if err != nil {
			dc.reportScriptError(err, res)
			return
		}
		dc.setMessage("ok", L("Data Client reiniciado.", "Data Client restarted."))
	case "autostart-on", "autostart-off":
		dc.mu.Lock()
		dc.prefs.AutoStart = name == "autostart-on"
		dc.savePrefsLocked()
		dc.mu.Unlock()
	case "refresh":
		// os detectores rodam logo depois da ação
	case "openlog":
		if st.LogPath == "" {
			dc.setMessage("warn", L("Ainda não achei o arquivo de log do Data Client.", "Data Client log file not found yet."))
			return
		}
		if err := openFileInEditor(st.LogPath); err != nil {
			dc.setMessage("err", L("Não consegui abrir o log: ", "Could not open the log: ")+err.Error())
		}
	case "opendata":
		if err := openFolder(dc.app.dataDir); err != nil {
			dc.setMessage("err", L("Não consegui abrir a pasta: ", "Could not open the folder: ")+err.Error())
		}
	default:
		dc.setMessage("err", L("Ação desconhecida: ", "Unknown action: ")+name)
	}
}

func (dc *DataClient) reportScriptError(err error, result string) {
	switch {
	case errors.Is(err, ErrCancelled):
		dc.setMessage("warn", L("Você recusou a permissão do Windows. Nada foi alterado.", "You declined the Windows permission. Nothing was changed."))
	case errors.Is(err, ErrNotWindows):
		dc.setMessage("warn", L("Essa ação só funciona no Windows.", "This action only works on Windows."))
	default:
		msg := err.Error()
		if tail := lastLines(result, 3); tail != "" {
			msg += " — " + tail
		}
		dc.setMessage("err", msg)
	}
}

func lastLines(s string, n int) string {
	var keep []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r", ""), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || l == "INICIO" || l == "OK" || strings.HasPrefix(l, "ERRO_") {
			continue
		}
		keep = append(keep, l)
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	return strings.Join(keep, " · ")
}

var unsafeBatchChars = regexp.MustCompile(`[%^&|<>"!]`)

// runTaskScript recria a tarefa de início automático (a mesma que o
// instalador oficial cria) com os argumentos pedidos e reinicia o Data Client.
func (dc *DataClient) runTaskScript(exe, args string, restart bool) (string, error) {
	if unsafeBatchChars.MatchString(exe) {
		return "", fmt.Errorf(L("o caminho do Data Client tem caracteres que o script não aceita: %s", "the Data Client path has characters the script does not accept: %s"), exe)
	}
	resultPath := filepath.Join(dc.app.dataDir, "ligacao-resultado.txt")
	_ = os.Remove(resultPath)
	var b strings.Builder
	w := func(line string) { b.WriteString(line + "\r\n") }
	w("@echo off")
	w("chcp 65001 >nul")
	w(`set "RES=` + resultPath + `"`)
	w(`> "%RES%" echo INICIO`)
	w(`schtasks /Create /F /SC ONLOGON /RL HIGHEST /TN "` + taskName + `" /TR "\"` + exe + `\" ` + args + `" < nul >> "%RES%" 2>&1`)
	w("if errorlevel 1 (")
	w(`  >> "%RES%" echo ERRO_TAREFA`)
	w("  exit /b 2")
	w(")")
	if restart {
		w(`taskkill /F /IM albiondata-client.exe < nul >> "%RES%" 2>&1`)
		w("ping -n 3 127.0.0.1 >nul")
		w(`schtasks /Run /TN "` + taskName + `" < nul >> "%RES%" 2>&1`)
		w("if errorlevel 1 (")
		w(`  >> "%RES%" echo ERRO_INICIAR`)
		w("  exit /b 3")
		w(")")
	}
	w(`>> "%RES%" echo OK`)
	w("exit /b 0")
	return dc.runScript("ligar-data-client.cmd", b.String(), resultPath)
}

// runRestartScript fecha e reabre o Data Client com permissão de administrador.
func (dc *DataClient) runRestartScript(exe string, viaTask bool) (string, error) {
	if unsafeBatchChars.MatchString(exe) {
		return "", fmt.Errorf(L("o caminho do Data Client tem caracteres que o script não aceita: %s", "the Data Client path has characters the script does not accept: %s"), exe)
	}
	resultPath := filepath.Join(dc.app.dataDir, "ligacao-resultado.txt")
	_ = os.Remove(resultPath)
	var b strings.Builder
	w := func(line string) { b.WriteString(line + "\r\n") }
	w("@echo off")
	w("chcp 65001 >nul")
	w(`set "RES=` + resultPath + `"`)
	w(`> "%RES%" echo INICIO`)
	w(`taskkill /F /IM albiondata-client.exe < nul >> "%RES%" 2>&1`)
	w("ping -n 3 127.0.0.1 >nul")
	if viaTask {
		w(`schtasks /Run /TN "` + taskName + `" < nul >> "%RES%" 2>&1`)
		w("if errorlevel 1 (")
		w(`  >> "%RES%" echo ERRO_INICIAR`)
		w("  exit /b 3")
		w(")")
	} else {
		dir := dc.clientDir()
		_ = os.MkdirAll(dir, 0o755)
		w(`cd /d "` + dir + `"`)
		w(`start "" "` + exe + `" -p ` + dc.ingestURL)
	}
	w(`>> "%RES%" echo OK`)
	w("exit /b 0")
	return dc.runScript("reiniciar-data-client.cmd", b.String(), resultPath)
}

func (dc *DataClient) runScript(name, content, resultPath string) (string, error) {
	path := filepath.Join(dc.app.dataDir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	dc.app.logf("script elevado: %s", path)
	code, err := runElevatedScript(path, 90*time.Second)
	raw, _ := os.ReadFile(resultPath)
	result := string(raw)
	if err != nil {
		return result, err
	}
	switch code {
	case 0:
		return result, nil
	case 2:
		return result, errors.New(L("o Windows não deixou criar a tarefa de início automático", "Windows did not allow creating the autostart task"))
	case 3:
		return result, errors.New(L("a tarefa foi ajustada, mas o Data Client não abriu", "the task was updated, but the Data Client did not open"))
	default:
		return result, fmt.Errorf(L("o script terminou com código %d", "the script ended with code %d"), code)
	}
}

// ---------- log do Data Client ----------

var (
	ansiRe     = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	logLineRe  = regexp.MustCompile(`^([A-Z]{4,5})\[([^\]]*)\]\s?(.*)$`)
	reVersion  = regexp.MustCompile(`Starting Albion Data Client, version: (\S+)`)
	reLocation = regexp.MustCompile(`Updating player location to (\S+?)\.?$`)
	rePlayer   = regexp.MustCompile(`Updating player to (.+?)\.?$`)
	reServer   = regexp.MustCompile(`Successfully sent ingest request to https?://pow\.(west|east|europe)\.`)
	reSending  = regexp.MustCompile(`Sending (\d+) live market (sell|buy) orders`)
)

const maxLogLines = 160

func (dc *DataClient) pickLogFile() {
	dc.mu.Lock()
	exe := dc.st.Exe
	dc.mu.Unlock()
	best := ""
	var bestTime time.Time
	for _, p := range logCandidates(dc.app.dataDir, exe) {
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() {
			continue
		}
		if best == "" || fi.ModTime().After(bestTime) {
			best, bestTime = p, fi.ModTime()
		}
	}
	dc.mu.Lock()
	defer dc.mu.Unlock()
	if best != "" && best != dc.logPath {
		dc.logPath = best
		dc.logOffset = 0
		dc.resetLogStateLocked()
		dc.st.LogPath = best
	}
}

func (dc *DataClient) resetLogStateLocked() {
	dc.logLines = nil
	dc.newLines = nil
	dc.logReset = true
	dc.st.Version = ""
	dc.st.Location = ""
	dc.st.LocationRaw = ""
	dc.st.LocationAt = 0
	dc.st.Character = ""
	dc.st.NeedZone = false
	dc.st.CaptureErr = ""
	dc.st.Encrypted = 0
}

func (dc *DataClient) tailLog() {
	dc.mu.Lock()
	path, offset := dc.logPath, dc.logOffset
	dc.mu.Unlock()
	if path == "" {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return
	}
	size := fi.Size()
	fresh := false
	if size < offset {
		offset = 0
		fresh = true
	}
	if offset == 0 {
		fresh = true
	}
	const window = 2 << 20
	var header []byte
	if offset == 0 && size > window {
		header = make([]byte, 16<<10)
		n, _ := f.ReadAt(header, 0)
		header = header[:n]
		offset = size - window
	}
	if size == offset && !fresh {
		return
	}
	buf := make([]byte, size-offset)
	n, _ := f.ReadAt(buf, offset)
	buf = buf[:n]
	if offset > 0 && fresh {
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			offset += int64(i + 1)
			buf = buf[i+1:]
		}
	}
	end := bytes.LastIndexByte(buf, '\n')
	if end < 0 {
		if fresh {
			dc.mu.Lock()
			dc.resetLogStateLocked()
			dc.logOffset = offset
			dc.mu.Unlock()
		}
		return
	}
	chunk := buf[:end+1]
	dc.mu.Lock()
	defer dc.mu.Unlock()
	if path != dc.logPath {
		return
	}
	if fresh {
		dc.resetLogStateLocked()
	}
	if header != nil {
		for _, line := range strings.Split(string(header), "\n") {
			dc.parseLogLineLocked(line, false)
		}
	}
	for _, line := range strings.Split(string(chunk), "\n") {
		dc.parseLogLineLocked(line, true)
	}
	dc.logOffset = offset + int64(end+1)
}

func (dc *DataClient) parseLogLineLocked(raw string, keep bool) {
	line := strings.TrimSpace(ansiRe.ReplaceAllString(strings.TrimRight(raw, "\r"), ""))
	if line == "" {
		return
	}
	level, when, msg := "", "", line
	if m := logLineRe.FindStringSubmatch(line); m != nil {
		level, when, msg = m[1], m[2], strings.TrimSpace(m[3])
	}
	if level == "DEBU" || level == "TRAC" {
		return
	}
	at := time.Now().UnixMilli()
	if t, err := time.Parse(time.RFC3339, when); err == nil {
		at = t.UnixMilli()
	}
	switch {
	case reVersion.MatchString(msg):
		dc.st.Version = reVersion.FindStringSubmatch(msg)[1]
	case reLocation.MatchString(msg):
		loc := reLocation.FindStringSubmatch(msg)[1]
		dc.st.LocationRaw = loc
		dc.st.Location = cityFromLocation(loc)
		dc.st.LocationAt = at
		dc.st.NeedZone = false
	case rePlayer.MatchString(msg):
		dc.st.Character = rePlayer.FindStringSubmatch(msg)[1]
	case strings.Contains(msg, "location has not yet been set") || strings.Contains(msg, "location is not valid"):
		dc.st.NeedZone = true
	case strings.Contains(msg, "Market data is encrypted"):
		dc.st.Encrypted = at
	case strings.Contains(msg, "No packet capture driver was detected"),
		strings.Contains(msg, "admin-only mode"),
		strings.Contains(msg, "Only the outdated WinPcap"),
		strings.Contains(msg, "Packet capture has stopped due to an error"):
		dc.st.CaptureErr = msg
	case strings.Contains(msg, "Will listen to these devices"):
		dc.st.CaptureErr = ""
	}
	if m := reServer.FindStringSubmatch(msg); m != nil {
		dc.st.ServerHint = m[1]
	}
	if reSending.MatchString(msg) && at >= dc.startedAt.UnixMilli() {
		dc.st.PublicSends++
		dc.st.LastPublicSend = at
	}
	if !keep {
		return
	}
	stamp := ""
	if t, err := time.Parse(time.RFC3339, when); err == nil {
		stamp = t.Local().Format("15:04:05")
	}
	shown := strings.TrimSpace(stamp + " " + level + " " + msg)
	if len(shown) > 400 {
		shown = shown[:400] + "…"
	}
	dc.logLines = append(dc.logLines, shown)
	if len(dc.logLines) > maxLogLines {
		dc.logLines = dc.logLines[len(dc.logLines)-maxLogLines:]
	}
	dc.newLines = append(dc.newLines, shown)
}

// flushLog manda para a tela as linhas novas do log (ou o log inteiro,
// quando o arquivo mudou).
func (dc *DataClient) flushLog() {
	dc.mu.Lock()
	reset := dc.logReset
	var lines []string
	if reset {
		lines = append([]string(nil), dc.logLines...)
	} else {
		lines = dc.newLines
	}
	dc.newLines = nil
	dc.logReset = false
	dc.mu.Unlock()
	if len(lines) == 0 && !reset {
		return
	}
	dc.app.hub.Broadcast("log", map[string]any{"reset": reset, "lines": lines})
}
