// AlbionRefino: calculadora de refino e poções do Albion Online com preços
// ao vivo vindos do Albion Data Client oficial (opção -p).
package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

var version = "1.0.0"

//go:embed web
var webFiles embed.FS

func main() {
	port := flag.Int("port", 47115, "porta local do app (o Data Client manda os preços para ela)")
	dataFlag := flag.String("data", "", "pasta de dados do app")
	noWindow := flag.Bool("no-window", false, "não abre a janela (para testes)")
	noAutoQuit := flag.Bool("no-autoquit", false, "não fecha o app quando a janela fecha")
	noUpdate := flag.Bool("no-update", false, "não procura versão nova no GitHub ao abrir")
	updatedFrom := flag.String("updated-from", "", "uso interno: versão anterior, depois de atualizar")
	reuseWindow := flag.Bool("reuse-window", false, "uso interno: não abre janela nova, a aberta reconecta")
	flag.Parse()

	dataDir := *dataFlag
	if dataDir == "" {
		dataDir = defaultDataDir()
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		showErrorBox("Albion Refino", "Não consegui criar a pasta de dados:\n"+dataDir+"\n\n"+err.Error())
		os.Exit(1)
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/", *port)

	// Já tem um app aberto: só abre outra janela para ele.
	if alreadyRunning(*port) {
		if !*noWindow {
			req, _ := http.NewRequest(http.MethodPost, url+"api/open-window", nil)
			req.Header.Set("X-Albion-Refino", "1")
			if resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req); err == nil {
				resp.Body.Close()
			} else {
				_ = openAppWindow(url, filepath.Join(dataDir, "janela"))
			}
		}
		return
	}

	logFile, _ := os.Create(filepath.Join(dataDir, "app.log"))
	var logOut io.Writer = os.Stderr
	if logFile != nil {
		logOut = io.MultiWriter(os.Stderr, logFile)
		defer logFile.Close()
	}
	logger := log.New(logOut, "", log.LstdFlags)

	// Procura versão nova antes de ocupar a porta, para a nova abrir no lugar.
	cleanupOldExe()
	if !*noUpdate {
		updated, err := selfUpdate(logger)
		if err != nil {
			logger.Printf("atualização: %v", err)
		}
		if updated {
			return
		}
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		showErrorBox("Albion Refino", fmt.Sprintf("A porta %d já está em uso por outro programa, então o app não consegue receber os preços do Data Client.\n\n%v", *port, err))
		os.Exit(1)
	}

	web, err := fs.Sub(webFiles, "web")
	if err != nil {
		logger.Fatal(err)
	}

	app := &App{
		dataDir:      dataDir,
		port:         *port,
		origin:       fmt.Sprintf("http://127.0.0.1:%d", *port),
		book:         NewBook(filepath.Join(dataDir, "precos.json")),
		feed:         &Feed{},
		hub:          NewHub(),
		logger:       logger,
		web:          web,
		quit:         make(chan string, 1),
		settingsPath: filepath.Join(dataDir, "ajustes.json"),
		updatedFrom:  *updatedFrom,
		icons:        newIconCache(dataDir),
	}
	app.dc = NewDataClient(app)
	if err := app.book.Load(); err != nil {
		logger.Printf("preços salvos ilegíveis, começando do zero: %v", err)
	}
	app.loadSettings()
	logger.Printf("Albion Refino %s em %s (dados em %s)", version, url, dataDir)

	stop := make(chan struct{})
	go app.dc.Run(stop)
	go app.autosave(stop)
	go app.watchWindows(stop, !*noAutoQuit && !*noWindow)
	if !*noUpdate {
		go app.checkUpdates(stop)
	}

	srv := &http.Server{Handler: app.routes(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			logger.Printf("servidor: %v", err)
			app.Quit("erro no servidor")
		}
	}()

	if !*noWindow {
		if *reuseWindow {
			// Depois de atualizar, a janela antiga se reconecta sozinha. Se ela
			// não voltar (foi fechada), abre uma nova.
			go func() {
				time.Sleep(15 * time.Second)
				if !app.everSeen.Load() {
					app.openWindow()
				}
			}()
		} else {
			app.openWindow()
		}
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	var reason string
	select {
	case reason = <-app.quit:
	case <-sig:
		reason = "interrompido"
	}
	logger.Printf("fechando: %s", reason)
	close(stop)
	app.hub.CloseAll()
	app.dc.Shutdown()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_ = srv.Shutdown(shutdownCtx)
	cancel()
	if err := app.book.Save(); err != nil {
		logger.Printf("salvar preços: %v", err)
	}
	app.upd.mu.Lock()
	exe := app.upd.relExe
	app.upd.mu.Unlock()
	if exe != "" {
		ln.Close()
		if err := relaunch(exe, "-updated-from="+version, "-reuse-window"); err != nil {
			logger.Printf("atualização: não consegui abrir a versão nova: %v", err)
			rollbackRelease(exe)
			_ = relaunch(exe)
		}
	}
}

func alreadyRunning(port int) bool {
	c := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/api/ping", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var v struct {
		App string `json:"app"`
	}
	if json.NewDecoder(resp.Body).Decode(&v) != nil {
		return false
	}
	return v.App == "albion-refino"
}
