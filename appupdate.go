package main

import (
	"os"
	"sync"
	"time"
)

// UpdateStatus é o que a tela mostra sobre versão nova.
type UpdateStatus struct {
	Current   string `json:"current"`
	Latest    string `json:"latest,omitempty"`
	State     string `json:"state"` // "" | "available" | "installing" | "restarting" | "error"
	Error     string `json:"error,omitempty"`
	CheckedAt int64  `json:"checkedAt,omitempty"`
}

type updater struct {
	mu     sync.Mutex
	st     UpdateStatus
	rel    *releaseInfo
	relExe string // .exe já trocado, para reabrir ao sair
}

func (a *App) updateStatus() UpdateStatus {
	a.upd.mu.Lock()
	defer a.upd.mu.Unlock()
	s := a.upd.st
	s.Current = version
	return s
}

func (a *App) setUpdate(f func(*UpdateStatus)) {
	a.upd.mu.Lock()
	f(&a.upd.st)
	a.upd.mu.Unlock()
	a.hub.Broadcast("update", a.updateStatus())
}

// checkUpdates procura versão nova logo depois de abrir e a cada 30 minutos.
func (a *App) checkUpdates(stop <-chan struct{}) {
	wait, every := 20*time.Second, 30*time.Minute
	if os.Getenv("ALBION_UPDATE_URL") != "" {
		wait, every = time.Second, 3*time.Second // testes
	}
	for {
		select {
		case <-stop:
			return
		case <-time.After(wait):
		}
		wait = every
		a.upd.mu.Lock()
		busy := a.upd.st.State == "installing" || a.upd.st.State == "restarting"
		a.upd.mu.Unlock()
		if busy {
			continue
		}
		rel, err := latestRelease()
		if err != nil {
			a.logf("verificar atualização: %v", err)
			continue
		}
		a.upd.mu.Lock()
		a.upd.rel = rel
		a.upd.mu.Unlock()
		a.setUpdate(func(s *UpdateStatus) {
			s.CheckedAt = time.Now().UnixMilli()
			if rel != nil {
				s.Latest, s.State, s.Error = rel.Version, "available", ""
			}
		})
	}
}

// applyUpdate baixa e instala a versão nova e reinicia o app nela. A janela
// aberta se reconecta sozinha à versão nova.
func (a *App) applyUpdate() {
	a.upd.mu.Lock()
	if a.upd.st.State == "installing" || a.upd.st.State == "restarting" {
		a.upd.mu.Unlock()
		return
	}
	rel := a.upd.rel
	a.upd.mu.Unlock()
	a.setUpdate(func(s *UpdateStatus) { s.State, s.Error = "installing", "" })
	if rel == nil {
		r, err := latestRelease()
		if err != nil || r == nil {
			msg := "Você já está na versão mais nova."
			if err != nil {
				msg = "Não consegui falar com o GitHub: " + err.Error()
			}
			a.setUpdate(func(s *UpdateStatus) { s.State, s.Error = "error", msg })
			return
		}
		rel = r
	}
	exe, err := installRelease(rel)
	if err != nil {
		a.logf("atualização: %v", err)
		a.setUpdate(func(s *UpdateStatus) { s.State, s.Error = "error", err.Error() })
		return
	}
	a.upd.mu.Lock()
	a.upd.relExe = exe
	a.upd.mu.Unlock()
	a.setUpdate(func(s *UpdateStatus) { s.State, s.Latest = "restarting", rel.Version })
	a.logf("atualização: %s instalada, reiniciando", rel.Tag)
	time.Sleep(400 * time.Millisecond) // dá tempo da tela receber o aviso
	a.Quit("atualização para " + rel.Tag)
}
