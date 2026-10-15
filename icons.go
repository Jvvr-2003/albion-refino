package main

// Ícones dos itens: GET /img/{id} busca a imagem no serviço oficial do jogo
// (render.albiononline.com) uma vez e guarda em <pasta de dados>/icones, para
// as próximas vezes abrirem na hora e funcionarem sem internet.

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var reItemID = regexp.MustCompile(`^T[1-8]_[A-Z0-9_]{1,60}(@[1-4])?$`)

var pngMagic = []byte("\x89PNG\r\n\x1a\n")

func renderURL(id string) string {
	base := os.Getenv("ALBION_RENDER_URL") // só para testes
	if base == "" {
		base = "https://render.albiononline.com/v1/item/"
	}
	return base + id + ".png?size=64&quality=1"
}

type iconCache struct {
	dir    string
	client *http.Client
	mu     sync.Mutex
	failed map[string]time.Time // falhas recentes, para não insistir a cada tela
	locks  map[string]*sync.Mutex
}

func newIconCache(dataDir string) *iconCache {
	return &iconCache{
		dir:    filepath.Join(dataDir, "icones"),
		client: &http.Client{Timeout: 15 * time.Second},
		failed: map[string]time.Time{},
		locks:  map[string]*sync.Mutex{},
	}
}

// fileName troca o @ do encantamento, que alguns sistemas não gostam em nome de arquivo.
func (c *iconCache) fileName(id string) string {
	return filepath.Join(c.dir, strings.ReplaceAll(id, "@", "_E")+".png")
}

func (c *iconCache) lockFor(id string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	l := c.locks[id]
	if l == nil {
		l = &sync.Mutex{}
		c.locks[id] = l
	}
	return l
}

func (c *iconCache) get(id string) ([]byte, error) {
	path := c.fileName(id)
	if raw, err := os.ReadFile(path); err == nil && bytes.HasPrefix(raw, pngMagic) {
		return raw, nil
	}
	l := c.lockFor(id)
	l.Lock()
	defer l.Unlock()
	if raw, err := os.ReadFile(path); err == nil && bytes.HasPrefix(raw, pngMagic) {
		return raw, nil
	}
	c.mu.Lock()
	when, bad := c.failed[id]
	c.mu.Unlock()
	if bad && time.Since(when) < 10*time.Minute {
		return nil, fmt.Errorf("falhou há pouco")
	}
	raw, err := c.download(id)
	if err != nil {
		c.mu.Lock()
		c.failed[id] = time.Now()
		c.mu.Unlock()
		return nil, err
	}
	if err := os.MkdirAll(c.dir, 0o755); err == nil {
		_ = writeFileAtomic(path, raw)
	}
	return raw, nil
}

func (c *iconCache) download(id string) ([]byte, error) {
	req, _ := http.NewRequest(http.MethodGet, renderURL(id), nil)
	req.Header.Set("User-Agent", "AlbionRefino/"+version)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("render respondeu %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(raw, pngMagic) {
		return nil, fmt.Errorf("resposta não é PNG")
	}
	return raw, nil
}

func (a *App) handleIcon(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !reItemID.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	raw, err := a.icons.get(id)
	if err != nil {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "sem ícone", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=604800")
	_, _ = w.Write(raw)
}
