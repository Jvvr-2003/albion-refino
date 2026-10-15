package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Order é uma ordem do mercado já convertida (preço em prata).
type Order struct {
	ID     int64
	Item   string
	City   string
	Side   byte // 'o' = ordem de venda (offer), 'r' = ordem de compra (request)
	Price  float64
	Amount int64
}

// Side guarda o melhor preço de um lado do livro (venda ou compra).
type Side struct {
	P      float64      `json:"p"`           // melhor preço
	A      int64        `json:"a,omitempty"` // quantidade nesse preço (só ao vivo)
	T      int64        `json:"t"`           // quando foi visto (ms)
	S      string       `json:"s"`           // "l" = ao vivo (Data Client), "a" = API do AODP
	L      [][2]float64 `json:"l,omitempty"` // até 8 níveis [preço, quantidade] (só ao vivo)
	orders map[int64]Order
}

// Entry junta os dois lados de um item numa cidade.
type Entry struct {
	O *Side `json:"o,omitempty"`
	R *Side `json:"r,omitempty"`
}

// Book é o livro de preços do app, separado por servidor.
type Book struct {
	mu    sync.Mutex
	data  map[string]map[string]*Entry // servidor -> "ITEM|Cidade" -> entrada
	dirty bool
	path  string
}

const (
	mergeWindowMs = 120_000               // páginas da mesma busca se juntam
	keepForMs     = 21 * 24 * 3600 * 1000 // descarta preço com mais de 21 dias
	maxLevels     = 8
)

func NewBook(path string) *Book {
	return &Book{data: map[string]map[string]*Entry{}, path: path}
}

func bookKey(item, city string) string { return item + "|" + city }

func (b *Book) server(name string) map[string]*Entry {
	m := b.data[name]
	if m == nil {
		m = map[string]*Entry{}
		b.data[name] = m
	}
	return m
}

type bookFile struct {
	V       int                          `json:"v"`
	Saved   int64                        `json:"saved"`
	Servers map[string]map[string]*Entry `json:"servers"`
}

func (b *Book) Load() error {
	raw, err := os.ReadFile(b.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var f bookFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	b.mu.Lock()
	defer b.mu.Unlock()
	for srv, entries := range f.Servers {
		if _, ok := aodpHosts[srv]; !ok {
			continue
		}
		dst := b.server(srv)
		for k, e := range entries {
			if e == nil || !strings.Contains(k, "|") {
				continue
			}
			if e.O != nil && now-e.O.T > keepForMs {
				e.O = nil
			}
			if e.R != nil && now-e.R.T > keepForMs {
				e.R = nil
			}
			if e.O == nil && e.R == nil {
				continue
			}
			dst[k] = e
		}
	}
	return nil
}

func (b *Book) Save() error {
	b.mu.Lock()
	if !b.dirty {
		b.mu.Unlock()
		return nil
	}
	raw, err := json.Marshal(bookFile{V: 1, Saved: time.Now().UnixMilli(), Servers: b.data})
	b.dirty = false
	b.mu.Unlock()
	if err != nil {
		return err
	}
	return writeFileAtomic(b.path, raw)
}

// MarshalAll devolve o livro inteiro em JSON (servidor -> chave -> entrada).
func (b *Book) MarshalAll() json.RawMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	raw, err := json.Marshal(b.data)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

// ApplyLive grava ordens recebidas do Data Client e devolve as entradas que mudaram.
func (b *Book) ApplyLive(server string, orders []Order, now int64) map[string]*Entry {
	type groupKey struct {
		key  string
		side byte
	}
	groups := map[groupKey][]Order{}
	var keys []groupKey
	for _, o := range orders {
		if o.Item == "" || o.City == "" || o.Price <= 0 {
			continue
		}
		k := groupKey{bookKey(o.Item, o.City), o.Side}
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], o)
	}
	if len(keys) == 0 {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	srv := b.server(server)
	changed := map[string]*Entry{}
	for _, k := range keys {
		e := srv[k.key]
		if e == nil {
			e = &Entry{}
			srv[k.key] = e
		}
		cur := &e.O
		if k.side == 'r' {
			cur = &e.R
		}
		merged := map[int64]Order{}
		if s := *cur; s != nil && s.S == "l" && s.orders != nil && now-s.T <= mergeWindowMs {
			for id, o := range s.orders {
				merged[id] = o
			}
		}
		for _, o := range groups[k] {
			merged[o.ID] = o
		}
		*cur = summarize(merged, k.side, now)
		changed[k.key] = &Entry{O: e.O, R: e.R}
	}
	b.dirty = true
	return changed
}

// summarize calcula melhor preço, quantidade nele e os primeiros níveis.
func summarize(orders map[int64]Order, side byte, now int64) *Side {
	byPrice := map[float64]int64{}
	for _, o := range orders {
		byPrice[o.Price] += o.Amount
	}
	prices := make([]float64, 0, len(byPrice))
	for p := range byPrice {
		prices = append(prices, p)
	}
	if side == 'r' {
		sort.Sort(sort.Reverse(sort.Float64Slice(prices)))
	} else {
		sort.Float64s(prices)
	}
	s := &Side{T: now, S: "l", orders: orders}
	if len(prices) == 0 {
		return s
	}
	s.P = prices[0]
	s.A = byPrice[prices[0]]
	for i, p := range prices {
		if i >= maxLevels {
			break
		}
		s.L = append(s.L, [2]float64{p, float64(byPrice[p])})
	}
	return s
}

// apiRow é uma linha de /api/v2/stats/prices do AODP.
type apiRow struct {
	ItemID      string  `json:"item_id"`
	City        string  `json:"city"`
	Quality     int     `json:"quality"`
	SellMin     float64 `json:"sell_price_min"`
	SellMinDate string  `json:"sell_price_min_date"`
	BuyMax      float64 `json:"buy_price_max"`
	BuyMaxDate  string  `json:"buy_price_max_date"`
}

func parseAPIDate(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "0001") {
		return 0
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t.UnixMilli()
		}
	}
	return 0
}

// ApplyAPI grava preços da API. Fica o dado mais recente: um preço ao vivo
// mais novo que o da API não é trocado.
func (b *Book) ApplyAPI(server string, rows []apiRow) (map[string]*Entry, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	srv := b.server(server)
	changed := map[string]*Entry{}
	applied := 0
	now := time.Now().UnixMilli()
	for _, r := range rows {
		if r.Quality > 1 || r.ItemID == "" {
			continue
		}
		city := cityFromAPI(r.City)
		if city == "" {
			continue
		}
		key := bookKey(r.ItemID, city)
		e := srv[key]
		touched := false
		if r.SellMin > 0 {
			if t := parseAPIDate(r.SellMinDate); t > 0 && t <= now+60_000 {
				if e == nil {
					e = &Entry{}
					srv[key] = e
				}
				if e.O == nil || t > e.O.T {
					e.O = &Side{P: r.SellMin, T: t, S: "a"}
					touched = true
				}
				applied++
			}
		}
		if r.BuyMax > 0 {
			if t := parseAPIDate(r.BuyMaxDate); t > 0 && t <= now+60_000 {
				if e == nil {
					e = &Entry{}
					srv[key] = e
				}
				if e.R == nil || t > e.R.T {
					e.R = &Side{P: r.BuyMax, T: t, S: "a"}
					touched = true
				}
				applied++
			}
		}
		if touched {
			changed[key] = &Entry{O: e.O, R: e.R}
		}
	}
	if len(changed) > 0 {
		b.dirty = true
	}
	return changed, applied
}

// FeedLine e FeedItem alimentam a lista "Últimas capturas".
type FeedLine struct {
	ID string  `json:"id"`
	P  float64 `json:"p"`
	A  int64   `json:"a"`
}

type FeedItem struct {
	T     int64      `json:"t"`
	City  string     `json:"c"`
	Loc   string     `json:"loc,omitempty"`
	Side  string     `json:"s"`
	N     int        `json:"n"`
	Items []FeedLine `json:"i"`
}

type Feed struct {
	mu    sync.Mutex
	items []FeedItem
}

const feedMax = 40

func (f *Feed) Add(it FeedItem) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items = append([]FeedItem{it}, f.items...)
	if len(f.items) > feedMax {
		f.items = f.items[:feedMax]
	}
}

func (f *Feed) List() []FeedItem {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]FeedItem, len(f.items))
	copy(out, f.items)
	return out
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
