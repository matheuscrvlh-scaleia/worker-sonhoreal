# worker-fdc no servidor da Sonho Real

Instalado em 10/11/2026 no servidor do ERP (acesso pelo AnyDesk). Antes rodava no PC da Leila
lendo uma cópia de backup de 23/10/2026; aquela instalação deve ficar desinstalada
(`C:\worker-fdc\worker-fdc-agente.exe desinstalar` no PC dela), senão os dois gravam no Supabase.

Senhas e token **não** ficam aqui: estão no `config.json` do servidor e no `.env`.

## Onde está cada coisa (servidor)

| Caminho | O que é |
|---|---|
| `C:\worker-fdc` | clone deste repositório (branch `worker-views`). Só para compilar e puxar o `queries-views.yaml`. |
| `C:\instalador-fdc` | pasta de onde o `instalar.ps1` roda: `.exe` compilados aqui + `deploy\*` + `queries.yaml`. |
| `C:\worker-fdc-servico` | instalação em uso: `config.json`, `queries.yaml`, `logs\worker.log`, `logs\agente.log`. |
| `C:\Sistemas\FDCMarket\dados\FDCMARKET.GDB` | banco do ERP (Firebird 3.0.10, charset NONE). |
| `C:\Program Files\Firebird\Firebird_3_0\isql.exe` | cliente de linha de comando do Firebird. |

O `C:\worker-fdc` é o clone e não a instalação, por isso o instalador roda com
`-Destino C:\worker-fdc-servico`.

## config.json (seção banco)

```json
"host": "127.0.0.1", "porta": 3050,
"arquivo": "C:/Sistemas/FDCMarket/dados/FDCMARKET.GDB",
"usuario": "fdcrelatorio", "charset": "WIN1252", "opcoes": ""
```

O caminho vai com `/` (com `\` o JSON quebra). O token do Supabase é o `FDC_TOKEN_WORKER` do `.env`.

## Usuário do banco e views

A FDC deu o usuário `fdcrelatorio`, que só lê as views `VIEWPRODUTOS`, `VIEWESTOQUE`,
`VIEWCLIENTES` e `VIEWFATURAMENTO`. Por isso o worker usa o `queries-views.yaml` (copiado como
`queries.yaml`), que copia só produto, estoque, cliente e compra. Parcelas, fotos, condições,
promoções e lojas ficam paradas no Supabase em 23/10/2026 (ver `docs/INTEGRACAO.md`, seção de
situação dos dados).

Na `VIEWFATURAMENTO` do servidor o número da venda é `NUMEROREGISTRO` e o frete é `FRETE`
(na cópia de backup eram `NUMEROSAIDA` e `VALORFRETE`).

Para ter tudo, o admin do banco precisa dar SELECT ao `FDCRELATORIO` nestas 24 tabelas
(script pronto em `deploy/usuario_leitura.sql`, trocando `IA_LEITURA` por `FDCRELATORIO` e sem o
`CREATE USER`):

ASSISTENCIA, CLIENTE, CONDPGVENDA, CONDPGVENDAFATORFILIAL, CONDPGVENDAGRUPOFATOR, ESTOQUE,
FABRICANTE, FILIAL, GRUPO, ITEMPEDIDOVENDA, ITEMSAIDA, PEDIDOVENDA, PORTADOR, PRODUTO,
PRODUTOFOTO, PRODUTOMARCA, PRODUTOPROMOCAO, PRODUTOWEB, RECEBER, RECEBIDO, SAIDA, SITUACAO,
SUBGRUPO, TELEFONES.

Conferir o que o usuário enxerga (no `isql` como `fdcrelatorio`):

```sql
SELECT DISTINCT RDB$RELATION_NAME FROM RDB$USER_PRIVILEGES WHERE RDB$USER = CURRENT_USER;
```

Depois de liberado, voltar para o `queries.yaml` completo (cmd como administrador):

```
cd /d C:\worker-fdc
git checkout main
git pull
copy /y queries.yaml C:\instalador-fdc\queries.yaml
cd /d C:\instalador-fdc
powershell -ExecutionPolicy Bypass -File .\instalar.ps1 -Destino C:\worker-fdc-servico
```

## Serviço

Serviço do Windows `WorkerFDC` ("Worker FDC (Scale IA)"): início automático atrasado (sobe
depois do Firebird), reinicia sozinho 10 s após cair e roda sem ninguém logado.

```
C:\worker-fdc-servico\worker-fdc-agente.exe status | parar | iniciar | reiniciar
sc qc WorkerFDC
```

## Atualizar

- **Só o SQL** (`queries-views.yaml`): `git pull` em `C:\worker-fdc` e copiar para
  `C:\worker-fdc-servico\queries.yaml`. O worker relê sozinho; se o SQL novo der erro, ele segue
  com o anterior e o erro aparece no `/status`.
- **Código do worker**: o `updates.scaleia.ai` não existe e o workflow `publicar` nunca rodou,
  então não há atualização automática. Compilar no servidor (Go instalado pelo `winget`) e
  reinstalar:

```
cd /d C:\worker-fdc
git pull
set CGO_ENABLED=0
go build -ldflags "-s -w -X ponte/internal/versao.Versao=v2.0.0" -o C:\instalador-fdc\worker-fdc.exe .\cmd\worker-fdc
go build -ldflags "-s -w -X ponte/internal/versao.Versao=v2.0.0" -o C:\instalador-fdc\worker-fdc-agente.exe .\cmd\worker-fdc-agente
cd /d C:\instalador-fdc
powershell -ExecutionPolicy Bypass -File .\instalar.ps1 -Destino C:\worker-fdc-servico
```

## Conferir se está lendo o banco real

`POST /admin/comandos {"tipo":"consulta","consulta":"diag_ultimas_vendas"}` (só no
`queries-views.yaml`): as vendas devem ser de hoje ou ontem. Em 10/11/2026 a mais recente era a
venda 8264 da loja 01, do mesmo dia.
