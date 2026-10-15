package main

// Atualização automática: ao abrir, o app consulta a última Release do
// GitHub. Se ela for mais nova, baixa o AlbionRefino.exe, troca o arquivo
// que está rodando (renomeando o antigo para .old) e reabre a versão nova.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	updateRepo  = "Jvvr-2003/albion-refino"
	updateAsset = "AlbionRefino.exe"
)

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// parseVersion transforma "v1.0.12" ou "1.0.12" em [1 0 12].
func parseVersion(s string) ([]int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	parts := strings.Split(s, ".")
	if len(parts) == 0 {
		return nil, false
	}
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, false
		}
		out[i] = n
	}
	return out, true
}

// newerVersion diz se a versão b é mais nova que a.
func newerVersion(a, b string) bool {
	va, ok1 := parseVersion(a)
	vb, ok2 := parseVersion(b)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < len(va) || i < len(vb); i++ {
		x, y := 0, 0
		if i < len(va) {
			x = va[i]
		}
		if i < len(vb) {
			y = vb[i]
		}
		if y != x {
			return y > x
		}
	}
	return false
}

func updateAPIURL() string {
	if test := os.Getenv("ALBION_UPDATE_URL"); test != "" {
		return test // só para testes
	}
	return "https://api.github.com/repos/" + updateRepo + "/releases/latest"
}

// cleanupOldExe apaga o .old que sobra de uma atualização anterior.
func cleanupOldExe() {
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(exe + ".old")
	}
}

// releaseInfo é a última versão publicada no GitHub.
type releaseInfo struct {
	Tag     string // ex.: "v1.0.9"
	Version string // ex.: "1.0.9"
	URL     string
	Size    int64
}

// latestRelease consulta a última Release. Devolve nil quando não há versão
// mais nova que a atual (ou quando este é um build local).
func latestRelease() (*releaseInfo, error) {
	if _, ok := parseVersion(version); !ok {
		return nil, nil // build local ("dev"): não atualiza
	}
	client := &http.Client{Timeout: 6 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, updateAPIURL(), nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "AlbionRefino/"+version)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf(L("sem resposta do GitHub: %w", "no response from GitHub: %w"), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(L("GitHub respondeu %d", "GitHub answered %d"), resp.StatusCode)
	}
	var rel ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return nil, err
	}
	if !newerVersion(version, rel.TagName) {
		return nil, nil
	}
	for _, a := range rel.Assets {
		if a.Name == updateAsset {
			return &releaseInfo{Tag: rel.TagName, Version: strings.TrimPrefix(rel.TagName, "v"), URL: a.URL, Size: a.Size}, nil
		}
	}
	return nil, fmt.Errorf(L("a release %s não tem o %s", "release %s does not have %s"), rel.TagName, updateAsset)
}

// installRelease baixa a versão nova e troca o .exe em uso por ela.
// Devolve o caminho do .exe (já com a versão nova).
func installRelease(rel *releaseInfo) (string, error) {
	dl := &http.Client{Timeout: 3 * time.Minute}
	r2, err := dl.Get(rel.URL)
	if err != nil {
		return "", fmt.Errorf(L("download falhou: %w", "download failed: %w"), err)
	}
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		return "", fmt.Errorf(L("download respondeu %d", "download answered %d"), r2.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(r2.Body, 100<<20))
	if err != nil {
		return "", err
	}
	if (rel.Size > 0 && int64(len(data)) != rel.Size) || len(data) < 1<<20 || !bytes.HasPrefix(data, exeMagic()) {
		return "", errors.New(L("o arquivo baixado não parece um .exe válido", "the downloaded file does not look like a valid .exe"))
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	newPath, oldPath := exe+".new", exe+".old"
	if err := os.WriteFile(newPath, data, 0o755); err != nil {
		return "", fmt.Errorf(L("não consegui gravar a versão nova na pasta do app: %w", "could not write the new version in the app folder: %w"), err)
	}
	_ = os.Remove(oldPath)
	// No Windows dá para renomear o .exe que está rodando, mas não sobrescrever.
	if err := os.Rename(exe, oldPath); err != nil {
		_ = os.Remove(newPath)
		return "", fmt.Errorf(L("não consegui trocar o .exe: %w", "could not replace the .exe: %w"), err)
	}
	if err := os.Rename(newPath, exe); err != nil {
		_ = os.Rename(oldPath, exe)
		return "", fmt.Errorf(L("não consegui trocar o .exe: %w", "could not replace the .exe: %w"), err)
	}
	return exe, nil
}

// rollbackRelease volta para o .exe antigo quando a versão nova não abre.
func rollbackRelease(exe string) {
	_ = os.Rename(exe, exe+".new")
	_ = os.Rename(exe+".old", exe)
}

// relaunch abre o .exe de novo com os mesmos argumentos, mais os extras.
func relaunch(exe string, extra ...string) error {
	var args []string
	for _, a := range os.Args[1:] {
		if strings.HasPrefix(a, "-updated-from") || strings.HasPrefix(a, "-reuse-window") {
			continue
		}
		args = append(args, a)
	}
	args = append(args, extra...)
	cmd := exec.Command(exe, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Start()
}

// selfUpdate roda ao abrir o app. Devolve true quando baixou uma versão nova e
// já a abriu; nesse caso quem chamou deve sair.
func selfUpdate(logger *log.Logger) (bool, error) {
	rel, err := latestRelease()
	if err != nil || rel == nil {
		return false, err
	}
	logger.Printf("atualização: %s -> %s", version, rel.Tag)
	exe, err := installRelease(rel)
	if err != nil {
		return false, err
	}
	if err := relaunch(exe, "-updated-from="+version); err != nil {
		rollbackRelease(exe)
		return false, fmt.Errorf(L("não consegui abrir a versão nova: %w", "could not open the new version: %w"), err)
	}
	logger.Printf("atualização: versão %s aberta, fechando a %s", rel.Tag, version)
	return true, nil
}

// exeMagic é o começo de um executável válido nesta plataforma.
func exeMagic() []byte {
	if runtime.GOOS == "windows" {
		return []byte("MZ")
	}
	return []byte("\x7fELF")
}
