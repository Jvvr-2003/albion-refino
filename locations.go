package main

import (
	"regexp"
	"strconv"
	"strings"
)

// locationNames converte o ID de local que o Data Client manda nas ordens
// para o nome do mercado. A tabela é a mesma do Albion Online Data Project
// (AlbionData.Models/Locations.cs). Os portais e o segundo mercado de
// Caerleon têm o mesmo livro de ofertas da cidade, então caem na cidade,
// igual ao que o deduper do AODP faz.
var locationNames = map[int]string{
	7:    "Thetford",
	301:  "Thetford",
	1002: "Lymhurst",
	1301: "Lymhurst",
	2004: "Bridgewatch",
	2301: "Bridgewatch",
	3008: "Martlock",
	3301: "Martlock",
	4002: "Fort Sterling",
	4301: "Fort Sterling",
	3005: "Caerleon",
	3013: "Caerleon",
	5003: "Brecilien",
	3003: "Black Market",
	4:    "Swamp Cross",
	8:    "Morgana's Rest",
	1006: "Forest Cross",
	1012: "Merlyn's Rest",
	2002: "Steppe Cross",
	3002: "Highland Cross",
	4006: "Mountain Cross",
	4300: "Arthur's Rest",
}

// apiCityNames são os nomes que a API do AODP devolve no campo "city".
var apiCityNames = map[string]string{
	"thetford":      "Thetford",
	"fort sterling": "Fort Sterling",
	"fortsterling":  "Fort Sterling",
	"lymhurst":      "Lymhurst",
	"bridgewatch":   "Bridgewatch",
	"martlock":      "Martlock",
	"caerleon":      "Caerleon",
	"brecilien":     "Brecilien",
	"black market":  "Black Market",
	"blackmarket":   "Black Market",
}

var leadingDigits = regexp.MustCompile(`^(\d{1,6})`)

// cityFromLocation devolve o nome do mercado para um LocationId do jogo, ou
// "" quando o local não é um mercado conhecido (esconderijos, ilhas etc.).
func cityFromLocation(loc string) string {
	s := strings.TrimSpace(loc)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToUpper(s), "BLACKBANK") {
		return "Black Market"
	}
	if m := leadingDigits.FindStringSubmatch(s); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			if name, ok := locationNames[n]; ok {
				return name
			}
		}
	}
	return ""
}

// cityFromAPI normaliza o nome de cidade que vem da API do AODP.
func cityFromAPI(city string) string {
	key := strings.ToLower(strings.TrimSpace(city))
	if name, ok := apiCityNames[key]; ok {
		return name
	}
	if name := cityFromLocation(city); name != "" {
		return name
	}
	return ""
}

// normalizeItemID deixa o ID no formato da API (T4_PLANKS_LEVEL1@1).
func normalizeItemID(id string, enchant int) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if enchant > 0 && !strings.Contains(id, "@") {
		return id + "@" + strconv.Itoa(enchant)
	}
	return id
}
