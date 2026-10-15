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

// selfUpdate devolve true quando baixou uma versão nova e já a abriu; nesse
// caso quem chamou deve sair.
func selfUpdate(logger *log.Logger) (bool, error) {
	if _, ok := parseVersion(version); !ok {
		return false, nil // build local ("dev"): não atualiza
	}
	client := &http.Client{Timeout: 6 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, updateAPIURL(), nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "AlbionRefino/"+version)
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("sem resposta do GitHub: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("GitHub respondeu %d", resp.StatusCode)
	}
	var rel ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return false, err
	}
	if !newerVersion(version, rel.TagName) {
		return false, nil
	}
	assetURL, size := "", int64(0)
	for _, a := range rel.Assets {
		if a.Name == updateAsset {
			assetURL, size = a.URL, a.Size
		}
	}
	if assetURL == "" {
		return false, fmt.Errorf("a release %s não tem o %s", rel.TagName, updateAsset)
	}
	logger.Printf("atualização: %s -> %s", version, rel.TagName)

	dl := &http.Client{Timeout: 2 * time.Minute}
	r2, err := dl.Get(assetURL)
	if err != nil {
		return false, fmt.Errorf("download falhou: %w", err)
	}
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		return false, fmt.Errorf("download respondeu %d", r2.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(r2.Body, 100<<20))
	if err != nil {
		return false, err
	}
	if (size > 0 && int64(len(data)) != size) || len(data) < 1<<20 || !bytes.HasPrefix(data, exeMagic()) {
		return false, errors.New("o arquivo baixado não parece um .exe válido")
	}

	exe, err := os.Executable()
	if err != nil {
		return false, err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	newPath, oldPath := exe+".new", exe+".old"
	if err := os.WriteFile(newPath, data, 0o755); err != nil {
		return false, fmt.Errorf("não consegui gravar a versão nova na pasta do app: %w", err)
	}
	_ = os.Remove(oldPath)
	// No Windows dá para renomear o .exe que está rodando, mas não sobrescrever.
	if err := os.Rename(exe, oldPath); err != nil {
		_ = os.Remove(newPath)
		return false, fmt.Errorf("não consegui trocar o .exe: %w", err)
	}
	if err := os.Rename(newPath, exe); err != nil {
		_ = os.Rename(oldPath, exe)
		return false, fmt.Errorf("não consegui trocar o .exe: %w", err)
	}
	args := append([]string{}, os.Args[1:]...)
	args = append(args, "-updated-from="+version)
	cmd := exec.Command(exe, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		// volta para a versão antiga
		_ = os.Rename(exe, newPath)
		_ = os.Rename(oldPath, exe)
		return false, fmt.Errorf("não consegui abrir a versão nova: %w", err)
	}
	logger.Printf("atualização: versão %s aberta, fechando a %s", rel.TagName, version)
	return true, nil
}

// exeMagic é o começo de um executável válido nesta plataforma.
func exeMagic() []byte {
	if runtime.GOOS == "windows" {
		return []byte("MZ")
	}
	return []byte("\x7fELF")
}
