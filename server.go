package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type App struct {
	dataDir  string
	port     int
	origin   string
	book     *Book
	feed     *Feed
	hub      *Hub
	dc       *DataClient
	logger   *log.Logger
	web      fs.FS
	quit     chan string
	quitOnce sync.Once

	settingsMu   sync.Mutex
	settingsRaw  json.RawMessage
	settingsPath string
	updatedFrom  string
	upd          updater
	server       atomic.Value // string: servidor escolhido na tela

	byeAt    atomic.Int64
	lastSeen atomic.Int64
	everSeen atomic.Bool
	ingestN  atomic.Int64
	otherN   atomic.Int64
}

func (a *App) logf(format string, args ...any) {
	if a.logger != nil {
		a.logger.Printf(format, args...)
	}
}

func (a *App) Quit(reason string) {
	a.quitOnce.Do(func() { a.quit <- reason })
}

func (a *App) currentServer() string {
	if v, ok := a.server.Load().(string); ok && v != "" {
		return v
	}
	return "west"
}

// liveServer decide em qual servidor guardar os preços ao vivo: o que o log
// do Data Client mostra, ou o escolhido na tela.
func (a *App) liveServer() string {
	if hint := a.dc.Status().ServerHint; hint != "" {
		if _, ok := aodpHosts[hint]; ok {
			return hint
		}
	}
	return a.currentServer()
}

// ---------- SSE ----------

type Hub struct {
	mu      sync.Mutex
	clients map[chan []byte]struct{}
}

func NewHub() *Hub { return &Hub{clients: map[chan []byte]struct{}{}} }

func (h *Hub) Subscribe() chan []byte {
	ch := make(chan []byte, 128)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *Hub) Unsubscribe(ch chan []byte) {
	h.mu.Lock()
	if _, ok := h.clients[ch]; ok {
		delete(h.clients, ch)
		close(ch)
	}
	h.mu.Unlock()
}

// CloseAll derruba as telas conectadas (elas reconectam sozinhas).
func (h *Hub) CloseAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		delete(h.clients, ch)
		close(ch)
	}
}

func (h *Hub) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

func (h *Hub) BroadcastRaw(event string, payload []byte) {
	msg := []byte("event: " + event + "\ndata: " + string(payload) + "\n\n")
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- msg:
		default:
			// tela lenta demais: derruba e ela reconecta pegando o estado inteiro
			delete(h.clients, ch)
			close(ch)
		}
	}
}

func (h *Hub) Broadcast(event string, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	h.BroadcastRaw(event, raw)
}

// ---------- rotas ----------

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", a.handleIndex)
	mux.HandleFunc("GET /data.json", a.handleStatic("data.json", "application/json"))
	mux.HandleFunc("GET /icon.svg", a.handleStatic("icon.svg", "image/svg+xml"))
	mux.HandleFunc("GET /api/ping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"app": "albion-refino", "version": version})
	})
	mux.HandleFunc("GET /api/state", a.handleState)
	mux.HandleFunc("GET /api/events", a.handleEvents)
	mux.HandleFunc("PUT /api/settings", a.handleSettings)
	mux.HandleFunc("POST /api/aodp", a.handleAODP)
	mux.HandleFunc("POST /api/client/{action}", a.handleClientAction)
	mux.HandleFunc("POST /api/bye", a.handleBye)
	mux.HandleFunc("POST /api/update", func(w http.ResponseWriter, r *http.Request) {
		go a.applyUpdate()
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/open-window", func(w http.ResponseWriter, r *http.Request) {
		go a.openWindow()
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /ingest/{topic}", a.handleIngest)
	return a.guard(mux)
}

// guard aceita só pedidos locais: Host do próprio app, e nada de outro site
// mandando dados ou apertando botões pelo navegador.
func (a *App) guard(next http.Handler) http.Handler {
	allowedHosts := map[string]bool{
		fmt.Sprintf("127.0.0.1:%d", a.port): true,
		fmt.Sprintf("localhost:%d", a.port): true,
	}
	allowedOrigins := map[string]bool{
		fmt.Sprintf("http://127.0.0.1:%d", a.port): true,
		fmt.Sprintf("http://localhost:%d", a.port): true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedHosts[r.Host] {
			http.Error(w, "host não permitido", http.StatusForbidden)
			return
		}
		origin := r.Header.Get("Origin")
		if strings.HasPrefix(r.URL.Path, "/ingest/") {
			if origin != "" {
				http.Error(w, "origem não permitida", http.StatusForbidden)
				return
			}
		} else if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if origin != "" && !allowedOrigins[origin] {
				http.Error(w, "origem não permitida", http.StatusForbidden)
				return
			}
			if r.URL.Path != "/api/bye" && r.Header.Get("X-Albion-Refino") != "1" {
				http.Error(w, "cabeçalho ausente", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	raw, err := fs.ReadFile(a.web, "index.html")
	if err != nil {
		http.Error(w, "index.html não encontrado", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}

func (a *App) handleStatic(name, ctype string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := fs.ReadFile(a.web, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(raw)
	}
}

func (a *App) handleState(w http.ResponseWriter, r *http.Request) {
	a.settingsMu.Lock()
	settings := a.settingsRaw
	a.settingsMu.Unlock()
	if len(settings) == 0 {
		settings = json.RawMessage("null")
	}
	writeJSON(w, map[string]any{
		"version":     version,
		"updatedFrom": a.updatedFrom,
		"update":      a.updateStatus(),
		"now":         time.Now().UnixMilli(),
		"settings":    settings,
		"book":        a.book.MarshalAll(),
		"status":      a.dc.Status(),
		"feed":        a.feed.List(),
		"log":         a.dc.LogLines(),
	})
}

func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming não suportado", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	ch := a.hub.Subscribe()
	defer a.hub.Unsubscribe(ch)
	a.byeAt.Store(0)
	a.everSeen.Store(true)
	a.lastSeen.Store(time.Now().UnixMilli())

	fmt.Fprintf(w, "retry: 1500\nevent: hello\ndata: {\"version\":%q}\n\n", version)
	flusher.Flush()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			a.lastSeen.Store(time.Now().UnixMilli())
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if _, err := w.Write(msg); err != nil {
				return
			}
			flusher.Flush()
		case <-ping.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
			a.lastSeen.Store(time.Now().UnixMilli())
		}
	}
}

type settingsBody struct {
	Server string          `json:"server"`
	Lang   string          `json:"lang"`
	State  json.RawMessage `json:"state"`
}

// appLang é o idioma escolhido na tela ("pt" ou "en"), usado nas mensagens do servidor.
var appLang atomic.Value

// L devolve o texto no idioma do app.
func L(pt, en string) string {
	if v, _ := appLang.Load().(string); v == "en" {
		return en
	}
	return pt
}

func setLang(lang string) {
	if lang == "en" {
		appLang.Store("en")
	} else {
		appLang.Store("pt")
	}
}

func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var body settingsBody
	if err := json.Unmarshal(raw, &body); err != nil {
		http.Error(w, "JSON inválido", http.StatusBadRequest)
		return
	}
	if _, ok := aodpHosts[body.Server]; ok {
		a.server.Store(body.Server)
	}
	setLang(body.Lang)
	a.settingsMu.Lock()
	a.settingsRaw = json.RawMessage(raw)
	a.settingsMu.Unlock()
	if err := writeFileAtomic(a.settingsPath, raw); err != nil {
		a.logf("ajustes: %v", err)
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) loadSettings() {
	raw, err := os.ReadFile(a.settingsPath)
	if err != nil {
		return
	}
	var body settingsBody
	if json.Unmarshal(raw, &body) != nil {
		return
	}
	a.settingsRaw = json.RawMessage(raw)
	if _, ok := aodpHosts[body.Server]; ok {
		a.server.Store(body.Server)
	}
	setLang(body.Lang)
}

type aodpRequest struct {
	Server string   `json:"server"`
	IDs    []string `json:"ids"`
	Cities []string `json:"cities"`
}

func (a *App) handleAODP(w http.ResponseWriter, r *http.Request) {
	var req aodpRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "JSON inválido", http.StatusBadRequest)
		return
	}
	if _, ok := aodpHosts[req.Server]; !ok {
		req.Server = a.currentServer()
	}
	if len(req.IDs) == 0 || len(req.Cities) == 0 {
		writeJSON(w, map[string]any{"n": 0})
		return
	}
	if len(req.IDs) > 600 {
		req.IDs = req.IDs[:600]
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	rows, err := fetchAODP(ctx, req.Server, req.IDs, req.Cities)
	changed, applied := a.book.ApplyAPI(req.Server, rows)
	if len(changed) > 0 {
		a.hub.Broadcast("book", map[string]any{"server": req.Server, "entries": changed})
	}
	resp := map[string]any{"n": applied, "rows": len(rows), "changed": len(changed)}
	if err != nil {
		resp["err"] = err.Error()
		a.logf("aodp: %v", err)
	}
	writeJSON(w, resp)
}

func (a *App) handleClientAction(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	a.dc.Action(action)
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) handleBye(w http.ResponseWriter, r *http.Request) {
	a.byeAt.Store(time.Now().UnixMilli())
	w.WriteHeader(http.StatusNoContent)
}

// ---------- dados do Data Client ----------

type rawOrder struct {
	ID               int64   `json:"Id"`
	ItemTypeID       string  `json:"ItemTypeId"`
	ItemGroupTypeID  string  `json:"ItemGroupTypeId"`
	LocationID       any     `json:"LocationId"`
	QualityLevel     int     `json:"QualityLevel"`
	EnchantmentLevel int     `json:"EnchantmentLevel"`
	UnitPriceSilver  float64 `json:"UnitPriceSilver"`
	Amount           int64   `json:"Amount"`
	AuctionType      string  `json:"AuctionType"`
}

type marketUpload struct {
	Orders []rawOrder `json:"Orders"`
}

func locationString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmt.Sprintf("%04d", int64(t))
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

func (a *App) handleIngest(w http.ResponseWriter, r *http.Request) {
	topic := r.PathValue("topic")
	raw, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Responde 200 sempre que o corpo chegou, para o Data Client não registrar erro.
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "OK")
	if topic != "marketorders.ingest" {
		a.otherN.Add(1)
		return
	}
	var up marketUpload
	if err := json.Unmarshal(raw, &up); err != nil {
		a.logf("ingest: JSON inválido: %v", err)
		return
	}
	a.ingestOrders(up.Orders)
}

func (a *App) ingestOrders(in []rawOrder) {
	now := time.Now().UnixMilli()
	type feedKey struct{ city, loc, side string }
	var orders []Order
	feeds := map[feedKey]*FeedItem{}
	var feedOrder []feedKey
	seenInFeed := map[string]int{}
	lastCity := ""
	for _, o := range in {
		loc := locationString(o.LocationID)
		city := cityFromLocation(loc)
		side := byte('o')
		sideName := "o"
		if strings.EqualFold(o.AuctionType, "request") {
			side, sideName = 'r', "r"
		}
		item := normalizeItemID(o.ItemTypeID, o.EnchantmentLevel)
		price := o.UnitPriceSilver / 10000
		if item == "" || price <= 0 {
			continue
		}
		fk := feedKey{city, loc, sideName}
		fi := feeds[fk]
		if fi == nil {
			fi = &FeedItem{T: now, City: city, Side: sideName}
			if city == "" {
				fi.Loc = loc
			}
			feeds[fk] = fi
			feedOrder = append(feedOrder, fk)
		}
		fi.N++
		lineKey := fmt.Sprintf("%v|%s", fk, item)
		if idx, ok := seenInFeed[lineKey]; ok {
			l := &fi.Items[idx]
			better := (side == 'o' && price < l.P) || (side == 'r' && price > l.P)
			if better {
				l.P, l.A = price, o.Amount
			} else if price == l.P {
				l.A += o.Amount
			}
		} else if len(fi.Items) < 12 {
			seenInFeed[lineKey] = len(fi.Items)
			fi.Items = append(fi.Items, FeedLine{ID: item, P: price, A: o.Amount})
		}
		if city == "" {
			continue
		}
		lastCity = city
		id := o.ID
		if id == 0 {
			id = int64(price*7919) ^ o.Amount<<20
		}
		orders = append(orders, Order{ID: id, Item: item, City: city, Side: side, Price: price, Amount: o.Amount})
	}
	server := a.liveServer()
	changed := a.book.ApplyLive(server, orders, now)
	if len(changed) > 0 {
		a.hub.Broadcast("book", map[string]any{"server": server, "entries": changed, "live": true})
	}
	for _, k := range feedOrder {
		it := *feeds[k]
		a.feed.Add(it)
		a.hub.Broadcast("feed", it)
	}
	a.ingestN.Add(int64(len(in)))
	a.dc.NoteIngest(len(in), lastCity)
}

// ---------- janela e ciclo de vida ----------

func (a *App) url() string { return fmt.Sprintf("http://127.0.0.1:%d/", a.port) }

func (a *App) openWindow() {
	if err := openAppWindow(a.url(), filepath.Join(a.dataDir, "janela")); err != nil {
		a.logf("janela: %v", err)
	}
}

// watchWindows fecha o app quando a última janela fecha (ou depois de muito
// tempo sem nenhuma janela aberta).
func (a *App) watchWindows(stop <-chan struct{}, autoQuit bool) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	start := time.Now()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		if !autoQuit {
			continue
		}
		now := time.Now().UnixMilli()
		if a.hub.Count() > 0 {
			a.lastSeen.Store(now)
			continue
		}
		if bye := a.byeAt.Load(); bye > 0 && now-bye > 7000 {
			a.Quit("janela fechada")
			return
		}
		idleSince := a.lastSeen.Load()
		if idleSince == 0 {
			idleSince = start.UnixMilli()
		}
		if now-idleSince > int64(3*time.Hour/time.Millisecond) {
			a.Quit("nenhuma janela aberta há 3 horas")
			return
		}
	}
}

func (a *App) autosave(stop <-chan struct{}) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if err := a.book.Save(); err != nil {
				a.logf("salvar preços: %v", err)
			}
		}
	}
}
