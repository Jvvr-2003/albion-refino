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

Ao abrir, o app consulta a última Release (`update.go`). Se a tag for mais nova que a versão dele, baixa o `AlbionRefino.exe`, renomeia o atual para `.old`, coloca o novo no lugar e reabre. Com o app aberto, ele confere de novo a cada 30 minutos (`appupdate.go`): se sair versão nova, aparece um aviso com o botão **Atualizar agora**, que instala, reinicia o app e reconecta a mesma janela. Para pular tudo: `AlbionRefino.exe -no-update`.
