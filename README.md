# Albion Refino e Poções

**[Baixar o AlbionRefino.exe (versão mais nova)](https://github.com/Jvvr-2003/albion-refino/releases/latest/download/AlbionRefino.exe)** · [todas as versões](https://github.com/Jvvr-2003/albion-refino/releases)

App para Windows que calcula o lucro de refino e de poções do Albion Online com
preços ao vivo do mercado.

## Como os preços chegam

O Albion Data Client oficial tem a opção `-p <url>` ("Base URL to send PRIVATE
data to"): cada pacote que ele manda para o Albion Online Data Project também é
mandado por HTTP para essa URL. O app escuta em `http://127.0.0.1:47115/ingest`
e recebe `POST /ingest/marketorders.ingest` com o mesmo JSON do AODP
(`{"Orders":[...]}`, preço em prata × 10000).

A ligação é feita recriando a tarefa agendada "Albion Data Client" (a mesma que
o instalador cria: `/SC ONLOGON /RL HIGHEST`) com `-minimize -p <url>`. Quando
não há Data Client rodando, o app abre um com `-p` e fecha ao sair.

## O que a tela calcula

- **Refino e poções**: lucro por unidade e total, retorno de recursos, foco, taxa
  da estação, imposto e taxa de anúncio, com preços ao vivo ou da API.
- **Níveis de refino / Níveis de alquimia** (botão no topo, muda com a aba): o
  foco de cada receita sai dos níveis do Destiny Board.
  - Refino: 250 de eficiência por nível do nó do próprio tier + 30 por nível de
    cada nó T4 a T8 do mesmo recurso.
  - Alquimia: 30 por nível do Alquimista (maestria) + 30 por nível de cada
    especialização (15 poções e o álcool) + 250 por nível da especialização da
    própria poção.
  - Foco = base × 0,5^(eficiência ÷ 10.000).
- **Tabela de lucro com abas**:
  - **SPU**: lucro por unidade.
  - **SPF**: prata por foco, ou seja, o lucro extra do foco ÷ o foco gasto.
  - **Transmutação** (só refino): o caminho mais barato para chegar a cada
    recurso bruto, transmutando um ou mais passos a partir do tier ou
    encantamento anterior, contra o preço de venda dele. O custo de cada passo é
    a prata da receita do jogo mais a taxa da estação.
- **Receita com coração** (em "Preços em prata", só refino): de T4 a T8, até o
  .3, troca 1 recurso bruto pelo coração do recurso. Madeira usa o Coração
  Arbóreo, minério o Montanhoso, fibra o Videira, pelego o Bestial e pedra o
  Empedrado.
- **Venda por dia** (linha "Você vende" da tabela de cidades): média de
  unidades vendidas nos últimos 7 dias. Vem do histórico da API do AODP
  (`/api/v2/stats/history`), porque o Data Client só manda ordens, não vendas.

Receitas, foco base, valores de item, transmutações e corações vêm dos arquivos
do jogo ([ao-bin-dumps](https://github.com/ao-data/ao-bin-dumps): `items.xml`,
`localization.xml`) e ficam em `web/data.json`.

## Arquivos

| Arquivo | Papel |
| --- | --- |
| `main.go` | flags, instância única, servidor local, janela, saída |
| `server.go` | rotas, SSE, recepção das ordens, guarda de origem |
| `book.go` | livro de preços (ao vivo e API), junção de páginas, gravação |
| `aodp.go` | busca na API pública do AODP |
| `dataclient.go` | status do Data Client, leitura do log, ações e scripts |
| `sys_windows.go` | registro, tarefa agendada, UAC, processos, janela do Edge |
| `sys_other.go` | versão de teste para Linux/macOS |
| `locations.go` | IDs de local do jogo para nome de cidade |
| `update.go` | atualização ao abrir (troca o próprio .exe) |
| `appupdate.go` | checagem com o app aberto, botão Verificar atualização |
| `icons.go` | ícones dos itens, baixados e guardados em cache |
| `web/` | tela (HTML único), receitas (`data.json`) e ícone |
| `tools/fakeclient` | Data Client simulado para testes |

## Compilar

```
./build.sh
```

Sem dependências externas: só a biblioteca padrão do Go (1.22+).

## Atualizar e publicar

Só fazer commit e push na `main`. O GitHub Actions compila o `AlbionRefino.exe`
e publica uma Release nova com versão automática (1.0.1, 1.0.2, ...).
O exe mais recente fica em Releases, no lado direito da página do repositório.

Ao abrir, o app consulta a última Release (`update.go`). Se a tag for mais nova que a versão dele, baixa o `AlbionRefino.exe`, renomeia o atual para `.old`, coloca o novo no lugar e reabre. Com o app aberto, ele confere de novo a cada 30 minutos (`appupdate.go`): se sair versão nova, aparece um aviso com o botão **Atualizar agora**, que instala, reinicia o app e reconecta a mesma janela. Para não esperar os 30 minutos, o botão de setas ao lado da versão, no topo, confere na hora (`POST /api/update/check`). Para pular tudo: `AlbionRefino.exe -no-update`.
