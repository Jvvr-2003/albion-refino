// Data Client simulado, só para testar o app fora do Windows. Escreve um log
// no mesmo formato do albiondata-client e manda ordens para a URL de -p,
// do mesmo jeito que o uploader HTTP do Data Client oficial.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"time"
)

type order struct {
	ID               int64  `json:"Id"`
	ItemTypeID       string `json:"ItemTypeId"`
	ItemGroupTypeID  string `json:"ItemGroupTypeId"`
	LocationID       string `json:"LocationId"`
	QualityLevel     int    `json:"QualityLevel"`
	EnchantmentLevel int    `json:"EnchantmentLevel"`
	UnitPriceSilver  int64  `json:"UnitPriceSilver"`
	Amount           int    `json:"Amount"`
	AuctionType      string `json:"AuctionType"`
	Expires          string `json:"Expires"`
}

var logf *os.File

func logLine(level, color, msg string) {
	ts := time.Now().Format(time.RFC3339)
	line := fmt.Sprintf("\x1b[%sm%s\x1b[0m[%s] %-44s \n", color, level, ts, msg)
	fmt.Print(line)
	if logf != nil {
		clean := fmt.Sprintf("%s[%s] %-44s \n", level, ts, msg)
		logf.WriteString(clean)
	}
}

func info(msg string) { logLine("INFO", "36", msg) }

var nextID int64 = 11400000000

func mk(item string, ench int, loc string, price float64, amount int, side string) order {
	nextID++
	group := item
	if i := strings.Index(item, "@"); i >= 0 {
		group = item[:i]
	}
	return order{ID: nextID, ItemTypeID: item, ItemGroupTypeID: group, LocationID: loc, QualityLevel: 1,
		EnchantmentLevel: ench, UnitPriceSilver: int64(price * 10000), Amount: amount, AuctionType: side,
		Expires: time.Now().Add(30 * 24 * time.Hour).UTC().Format("2006-01-02T15:04:05.000000")}
}

func send(url string, orders []order, side string) {
	body, _ := json.Marshal(map[string]any{"Orders": orders})
	kind := "sell"
	if side == "request" {
		kind = "buy"
	}
	info(fmt.Sprintf("Sending %d live market %s orders to ingest (Identifier: 0f8f2d1c-4a7b-4f3e-9b55-%012d)", len(orders), kind, rand.Intn(1e9)))
	resp, err := http.Post(url+"/marketorders.ingest", "application/json", bytes.NewReader(body))
	if err != nil {
		logLine("ERRO", "31", fmt.Sprintf("Error while sending ingest with data: %v", err))
		return
	}
	resp.Body.Close()
	info("Successfully sent ingest request to " + url)
	info("Successfully sent ingest request to https://pow.west.albion-online-data.com")
}

func main() {
	priv := flag.String("p", "", "private ingest")
	flag.Bool("minimize", false, "")
	speed := flag.Duration("every", 4*time.Second, "intervalo entre buscas no mercado")
	flag.Parse()
	logf, _ = os.Create("albiondata-client.log")
	info("Starting Albion Data Client, version: 0.1.61")
	info("This is a third-party application and is in no way affiliated with Sandbox Interactive or Albion Online.")
	time.Sleep(500 * time.Millisecond)
	info("Will listen to these devices: [\\Device\\NPF_{C0FFEE00-1234-5678-9ABC-DEF012345678}]")
	time.Sleep(700 * time.Millisecond)
	info("Updating player location to 4002.")
	info("Updating player to Jvdar.")
	if *priv == "" {
		select {}
	}
	loc := "4002" // Fort Sterling
	steps := []func(){
		func() { // pesquisa "tábuas" na aba comprar
			send(*priv, []order{
				mk("T4_PLANKS", 0, loc, 221, 312, "offer"), mk("T4_PLANKS", 0, loc, 224, 1500, "offer"),
				mk("T3_PLANKS", 0, loc, 58, 2210, "offer"), mk("T3_PLANKS", 0, loc, 59, 900, "offer"),
				mk("T5_PLANKS", 0, loc, 610, 140, "offer"), mk("T4_PLANKS_LEVEL1@1", 1, loc, 488, 75, "offer"),
			}, "offer")
		},
		func() { // pesquisa "troncos"
			send(*priv, []order{
				mk("T4_WOOD", 0, loc, 47, 4100, "offer"), mk("T4_WOOD", 0, loc, 48, 9000, "offer"),
				mk("T5_WOOD", 0, loc, 132, 1700, "offer"), mk("T4_WOOD_LEVEL1@1", 1, loc, 166, 600, "offer"),
			}, "offer")
		},
		func() { // aba vender: ordens de compra
			send(*priv, []order{
				mk("T4_PLANKS", 0, loc, 205, 3000, "request"), mk("T4_PLANKS", 0, loc, 201, 800, "request"),
				mk("T4_WOOD", 0, loc, 41, 12000, "request"),
			}, "request")
		},
		func() { // mais uma página da mesma busca, preço caiu
			send(*priv, []order{mk("T4_PLANKS", 0, loc, 219, 40, "offer")}, "offer")
		},
		func() { // muda para Lymhurst
			info("Updating player location to 1002.")
			loc = "1002"
		},
		func() {
			send(*priv, []order{
				mk("T4_FIBER", 0, loc, 39, 5200, "offer"), mk("T3_CLOTH", 0, loc, 61, 800, "offer"), mk("T4_CLOTH", 0, loc, 215, 260, "offer"),
			}, "offer")
		},
	}
	for i := 0; ; i++ {
		time.Sleep(*speed)
		if i < len(steps) {
			steps[i]()
		}
	}
}
