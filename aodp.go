package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Hosts da API do Albion Online Data Project por servidor do jogo.
var aodpHosts = map[string]string{
	"west":   "https://west.albion-online-data.com",
	"europe": "https://europe.albion-online-data.com",
	"east":   "https://east.albion-online-data.com",
}

const aodpMaxURL = 3800 // a API aceita até 4096 caracteres

func aodpURLs(server string, ids, cities []string) []string {
	host := aodpHosts[server]
	if host == "" {
		host = aodpHosts["west"]
	}
	if test := os.Getenv("ALBION_AODP_HOST"); test != "" {
		host = strings.TrimRight(test, "/") // só para testes
	}
	escaped := make([]string, 0, len(cities))
	for _, c := range cities {
		escaped = append(escaped, strings.ReplaceAll(url.QueryEscape(c), "+", "%20"))
	}
	tail := ".json?locations=" + strings.Join(escaped, ",") + "&qualities=1"
	mk := func(list []string) string {
		return host + "/api/v2/stats/prices/" + strings.Join(list, ",") + tail
	}
	var urls []string
	var chunk []string
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || strings.ContainsAny(id, "/?#,% ") {
			continue
		}
		if len(chunk) > 0 && len(mk(append(chunk, id))) > aodpMaxURL {
			urls = append(urls, mk(chunk))
			chunk = nil
		}
		chunk = append(chunk, id)
	}
	if len(chunk) > 0 {
		urls = append(urls, mk(chunk))
	}
	return urls
}

var aodpClient = &http.Client{Timeout: 25 * time.Second}

// fetchAODP busca os preços atuais na API pública do AODP.
func fetchAODP(ctx context.Context, server string, ids, cities []string) ([]apiRow, error) {
	var all []apiRow
	for _, u := range aodpURLs(server, ids, cities) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return all, err
		}
		req.Header.Set("User-Agent", "AlbionRefino/"+version)
		req.Header.Set("Accept", "application/json")
		resp, err := aodpClient.Do(req)
		if err != nil {
			return all, fmt.Errorf(L("sem resposta da API do AODP: %w", "no response from the AODP API: %w"), err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if err != nil {
			return all, err
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			return all, fmt.Errorf(L("a API do AODP pediu para esperar um pouco (limite de consultas)", "the AODP API asked to wait a bit (rate limit)"))
		}
		if resp.StatusCode != http.StatusOK {
			return all, fmt.Errorf(L("a API do AODP respondeu %d", "the AODP API answered %d"), resp.StatusCode)
		}
		var rows []apiRow
		if err := json.Unmarshal(body, &rows); err != nil {
			return all, fmt.Errorf(L("resposta da API do AODP em formato inesperado", "AODP API response in an unexpected format"))
		}
		all = append(all, rows...)
	}
	return all, nil
}
