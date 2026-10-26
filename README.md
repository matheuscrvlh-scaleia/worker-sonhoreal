# Ponte ERP (Firebird) · Sonho Real

API local que roda na máquina do cliente, consulta o Firebird do ERP e responde à IA do WhatsApp.
É um único `.exe` sem dependências, só leitura, que se atualiza sozinho.

```
IA (nuvem) ──HTTPS──> Cloudflare Tunnel ──> ponte-api (127.0.0.1:8080) ──> Firebird (3050)
                                                ▲ supervisiona e atualiza
                                           ponte-agente (serviço Windows "PonteERP")
                                                │ a cada 5 min
                                                ▼
                                 updates.scaleia.ai (R2) ◄── GitHub Actions (git tag)
```

## Peças

| Arquivo | O que é |
|---|---|
| `queries.yaml` | **As consultas.** É aqui que se mexe no dia a dia. Recarrega sozinho, sem reiniciar. |
| `cmd/ponte-api` | API HTTP. `-testar` roda todas as consultas contra o banco real. |
| `cmd/ponte-agente` | Serviço do Windows: mantém a API no ar e aplica atualizações com rollback. |
| `cmd/ponte-assinar` | Gera chaves e assina o manifesto (usado no CI). |
| `deploy/` | `instalar.ps1`, `config.exemplo.json`, `usuario_leitura.sql`. |
| `arquivos/` | Dicionário do banco exportado do cliente (tabelas, colunas, PK/FK). |

> **Vai integrar a IA/n8n com a ponte?** Veja [docs/INTEGRACAO.md](docs/INTEGRACAO.md).

## Endpoints

Todos exigem `Authorization: Bearer <token>`, menos o `/health`.

| | |
|---|---|
| `GET /health` | 200 se o banco responde, 503 se não. Traz versão e SHA do `queries.yaml` carregado. |
| `GET /q` | Lista as consultas com descrição e parâmetros. **Use para gerar as tools da IA.** |
| `GET /q/{nome}?param=valor` | Executa. Também aceita `POST` com JSON. |
| `POST /admin/atualizar` | (`token_admin`) O agente busca atualização agora, sem esperar os 5 min. |
| `POST /admin/sql` | (`token_admin` + `sql_livre: true`) `{"sql": "SELECT ...", "params": [...]}`. Só para explorar. |

Resposta: `{"consulta", "total", "truncado", "ms", "linhas": [...]}`. Erros: `{"erro": "..."}`, com
400 para parâmetro inválido, 503 para banco fora do ar ou ocupado e 504 para timeout.

## Consultas (perguntas do WhatsApp)

| Pergunta | Consulta |
|---|---|
| "Quanto custa X?" / "Tem X?" | `buscar_produto` → `detalhe_produto`, `estoque_por_loja` |
| "Tem promoção?" | `promocoes_vigentes` |
| "Parcela em quantas vezes?" / "Fica quanto em 10x?" | `condicoes_pagamento`, `simular_parcelamento` |
| "Faz por R$ X?" | `negociar_preco` (o limite de desconto nunca é dito ao cliente) |
| "Já sou cliente?" | `cliente_por_telefone` (identifica) → `cliente_por_documento` (pede o CPF) |
| "Quanto devo?" / "Manda o boleto" | `parcelas_em_aberto` (com PIX copia e cola quando o ERP gera) |
| "O que comprei?" / "Cadê meu pedido?" / "Quando monta?" | `compras_cliente` (vendas em `SAIDA`) |
| "Onde fica a loja?" | `lojas` |
| (instalação) | `diag_*`, só com `token_admin` |

`pedidos_cliente` e `assistencias_cliente` existem, mas estão restritas ao `token_admin`: na Sonho
Real, `PEDIDOVENDA` e `ASSISTENCIA` estão vazias, e a IA diria "você não tem pedidos" a quem comprou.

Regra de privacidade: o telefone **identifica**, mas não **autentica**. Dado financeiro ou de pedido só
depois de o cliente informar o CPF.

## Antes de ligar para clientes reais ⚠️

As consultas rodam sem erro no banco real (backup de 23/10/2026, Firebird 3.0.10). O cabeçalho do
`queries.yaml` registra o que o diagnóstico mostrou (sim/não = `T`/`F`, `VALORPAGO` vazio,
tabelas vazias). Ainda falta confirmar:

1. **Título em aberto = `RECEBER.STATUS '0'`** (dedução: `1` são 387 mil títulos e R$ 73 mi de
   histórico; `0` são 11 mil e R$ 2,3 mi). Confirme com `diag_receber` (status quitado tem quase
   tudo `com_baixa`) e abra no ERP 2 ou 3 títulos do `diag_receber_amostra`. **Sem isso a IA pode
   cobrar o que já foi pago.**
2. **`negociar_preco`**: confirme com a loja o desconto máximo (`desconto_maximo_loja`, hoje 5%,
   usado quando o grupo não define `PERCDESCONTO`).
3. **Parcelamento**: todas as condições têm fator 1, ou seja, até 15x sem juros. Confira com a loja
   e com o `diag_flags`: se `CONDPGVENDAGRUPOFATOR` tiver linhas, há juros por grupo de produto
   que a simulação ainda não aplica.

## Instalação na máquina do cliente (uma vez)

1. Baixar `https://updates.scaleia.ai/bin/ponte-windows.zip` e extrair.
2. Rodar `usuario_leitura.sql` como SYSDBA (cria `IA_LEITURA` só com `SELECT`).
3. PowerShell como administrador, na pasta extraída:
   ```powershell
   powershell -ExecutionPolicy Bypass -File .\instalar.ps1 -TokenTunel <token do túnel>
   ```
   Na primeira vez ele cria `C:\ponte\config.json` (com tokens novos) e abre o Bloco de Notas. Preencha
   `banco` e `chave_publica` e rode de novo. Ele testa todas as consultas no banco real antes de
   instalar os serviços.
4. No Cloudflare: Public Hostname `sonhoreal-erp.scaleia.ai` → `http://localhost:8080`, protegido por
   Access com Service Token.

Firebird 3+ com autenticação legada: `"opcoes": "auth_plugin_name=Legacy_Auth&wire_crypt=false"`.
O caminho do `.FDB` vai com `/` ou `\\` no JSON.

## Dia a dia: publicar mudança

```bash
# editou queries.yaml ou o código
git tag v1.0.1 && git push --tags
```
O GitHub Actions testa, compila, assina e publica. Em até 5 minutos o agente:
- baixa e confere a assinatura ed25519 e o SHA-256 (sem os dois, não instala);
- troca a API, confere o `/health` e **volta para a versão anterior sozinho** se a nova não responder
  em 60s (a versão recusada não é tentada de novo);
- troca o `queries.yaml`; se a API recusar o arquivo novo, restaura o anterior.

Pressa? `curl -X POST -H "Authorization: Bearer <token_admin>" https://.../admin/atualizar`.

**Configurar uma vez:** gerar as chaves com `go run ./cmd/ponte-assinar chaves`. A privada vai no
secret `PONTE_CHAVE_PRIVADA` e a pública no `config.json` de cada cliente. Também vão nos secrets:
`R2_ACCESS_KEY_ID`, `R2_SECRET_ACCESS_KEY` e `R2_ENDPOINT`; e na variável `R2_BUCKET`, com o bucket
exposto em `updates.scaleia.ai`.

O `ponte-agente.exe` não se atualiza sozinho (muda raramente). Para trocá-lo, rode o `instalar.ps1`
de novo, por acesso remoto ou pelo SSH de emergência via túnel.

## Quando der problema

| Sintoma | Onde olhar |
|---|---|
| IA sem resposta | `GET /health`: `banco_ok`, `consultas_erro`, `versao` |
| Atualização não chegou | `C:\ponte\logs\agente.log` e `C:\ponte\estado.json` (versões recusadas) |
| Consulta com erro | `C:\ponte\logs\api.log` e `ponte-api.exe -testar` |
| Serviço | `ponte-agente.exe status`, ou `services.msc` → PonteERP |

O que já se recupera sozinho (testado): API morta é reiniciada em 1s; Firebird reiniciado não
derruba a consulta seguinte; Firebird fora devolve 503 e volta sozinho; `queries.yaml` quebrado é
ignorado; versão ruim volta para a anterior; agente morto leva a API junto (Job Object), sem
processo órfão.

## Desenvolvimento

```bash
go test ./...
go build ./cmd/...
ponte-api.exe -config caminho/config.json -testar
```
