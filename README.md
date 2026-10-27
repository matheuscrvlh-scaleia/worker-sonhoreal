# worker-fdc · Sonho Real

Copia o FDC, o ERP (Firebird) da Sonho Real, para o Supabase, onde a IA do WhatsApp consulta.
O worker é um `.exe` só, sem dependências, que só lê o ERP e se atualiza sozinho. Se o PC da
loja desligar, a IA continua respondendo com a última cópia.

```
Firebird ◄── worker-fdc (PC da loja) ──HTTPS──► Supabase (Edge Function "fdc" + Postgres + Storage) ◄── IA / n8n
                 ▲ supervisiona e atualiza
            worker-fdc-agente (serviço Windows "WorkerFDC") ◄── updates.scaleia.ai (R2) ◄── GitHub Actions
```

> **Vai integrar a IA/n8n?** Veja [docs/INTEGRACAO.md](docs/INTEGRACAO.md).
> **Como funciona por dentro, segurança e deploy do Supabase:** [docs/ARQUITETURA.md](docs/ARQUITETURA.md).

## Peças

| Arquivo | O que é |
|---|---|
| `queries.yaml` | **O que é copiado do ERP** (`sincronizar`) e os diagnósticos (`consultas`). Recarrega sozinho. |
| `supabase/migrations` | Tabelas da cópia (`fdc.*`) e as perguntas da IA (`fdc.q_*`). |
| `supabase/functions/fdc` | Edge Function: API da IA (`/q`), recebimento do worker (`/sync`), diagnóstico (`/admin`). |
| `cmd/worker-fdc` | O worker. `-testar` roda tudo contra o banco real; `-uma-vez` faz uma carga e sai. |
| `cmd/worker-fdc-agente` | Serviço do Windows: mantém o worker no ar e aplica atualizações com rollback. |
| `cmd/ponte-assinar` | Gera chaves e assina o manifesto (usado no CI). |
| `deploy/` | `instalar.ps1`, `config.exemplo.json`, `usuario_leitura.sql`. |
| `arquivos/` | Dicionário do banco exportado do cliente (tabelas, colunas, PK/FK). |

## Perguntas do WhatsApp → consulta

| Pergunta | Consulta |
|---|---|
| "Quanto custa X?" / "Tem X?" | `buscar_produto` → `detalhe_produto`, `estoque_por_loja`, `fotos_produto` |
| "Tem promoção?" | `promocoes_vigentes` |
| "Parcela em quantas vezes?" / "Fica quanto em 10x?" | `condicoes_pagamento`, `simular_parcelamento` |
| "Faz por R$ X?" | `negociar_preco` (o limite de desconto nunca é dito ao cliente) |
| "Já sou cliente?" | `cliente_por_telefone` (identifica) → `cliente_por_documento` (pede o CPF) |
| "Quanto devo?" / "Manda o boleto" | `parcelas_em_aberto` (com PIX copia e cola quando o ERP gera) |
| "O que comprei?" / "Cadê meu pedido?" / "Quando monta?" | `compras_cliente` (vendas em `SAIDA`) |
| "Onde fica a loja?" | `lojas` |

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
2. **`negociar_preco`**: confirme com a loja o desconto máximo (`desconto_maximo_loja`, hoje 5%, em
   `supabase/functions/fdc/consultas.ts`, usado quando o grupo não define `PERCDESCONTO`).
3. **Parcelamento**: todas as condições têm fator 1, ou seja, até 15x sem juros. Confira com a loja
   e com o `diag_flags`: se `CONDPGVENDAGRUPOFATOR` tiver linhas, há juros por grupo de produto
   que a simulação ainda não aplica.

## Instalação

**Supabase:** ver [docs/ARQUITETURA.md](docs/ARQUITETURA.md#deploy) (migração, secrets, deploy da função).

**Máquina do cliente (uma vez):**
1. Baixar `https://updates.scaleia.ai/bin/worker-fdc-windows.zip` e extrair.
2. Banco novo: rodar `usuario_leitura.sql` como SYSDBA (cria `IA_LEITURA` só com `SELECT`).
3. PowerShell como administrador, na pasta extraída:
   ```powershell
   powershell -ExecutionPolicy Bypass -File .\instalar.ps1 -TokenWorker <FDC_TOKEN_WORKER do .env>
   ```
   Na primeira vez ele cria `C:\worker-fdc\config.json` (com o token do worker e, se achar
   `C:\ponte`, o banco da instalação antiga), mostra o comando para cadastrar o token no Supabase e
   abre o Bloco de Notas. Depois rode de novo: ele testa banco, Supabase e todas as extrações, e só
   então remove o `PonteERP` e o `cloudflared` antigos e instala o serviço `WorkerFDC`.

Firebird 3+ com autenticação legada: `"opcoes": "auth_plugin_name=Legacy_Auth&wire_crypt=false"`.
O caminho do `.FDB` vai com `/` ou `\\` no JSON.

**Onde instalar:** de preferência no servidor do ERP, que fica sempre ligado. O worker só precisa
de saída HTTPS (443).

## Dia a dia: publicar mudança

```bash
# editou queries.yaml ou o código do worker
git tag v2.0.1 && git push --tags
```
O GitHub Actions testa, compila, assina e publica. Em até 5 minutos o agente:
- baixa e confere a assinatura ed25519 e o SHA-256 (sem os dois, não instala);
- troca o worker, confere o `/health` local e **volta para a versão anterior sozinho** se a nova
  não responder em 60s (a versão recusada não é tentada de novo);
- troca o `queries.yaml`; se o worker recusar o arquivo novo, restaura o anterior.

Pressa? `POST /admin/comandos {"tipo": "atualizar"}` na Edge Function (token admin).

Mudança só no Supabase (`supabase/`) não passa pela tag: `npx supabase db push` e
`npx supabase functions deploy fdc --no-verify-jwt`.

**Configurar uma vez:** gerar as chaves com `go run ./cmd/ponte-assinar chaves`. A privada vai no
secret `PONTE_CHAVE_PRIVADA` e a pública no `config.json` de cada cliente. Também vão nos secrets:
`R2_ACCESS_KEY_ID`, `R2_SECRET_ACCESS_KEY` e `R2_ENDPOINT`; e na variável `R2_BUCKET`, com o bucket
exposto em `updates.scaleia.ai`.

O `worker-fdc-agente.exe` não se atualiza sozinho (muda raramente). Para trocá-lo, rode o
`instalar.ps1` de novo por acesso remoto.

## Quando der problema

| Sintoma | Onde olhar |
|---|---|
| IA com dado velho | `GET /status` na Edge Function: `worker_visto_em`, `erros`, `tabelas[].ultima_sync` |
| Uma tabela não atualiza | `erros` no `/status` (com token) ou `C:\worker-fdc\logs\worker.log` |
| "ia apagar N de M linhas" | trava de segurança: o SQL mudou ou o banco não é o de produção. Nada foi apagado. |
| Atualização não chegou | `C:\worker-fdc\logs\agente.log` e `C:\worker-fdc\estado.json` (versões recusadas) |
| Extração com erro | `worker-fdc.exe -testar` na máquina do cliente |
| Serviço | `worker-fdc-agente.exe status`, ou `services.msc` → WorkerFDC |

O que se recupera sozinho: worker morto é reiniciado em 1s; Firebird reiniciado não derruba o
ciclo seguinte; Supabase fora → o ciclo falha e o próximo manda tudo o que ficou para trás;
`queries.yaml` quebrado é ignorado; versão ruim volta para a anterior; agente morto leva o worker
junto (Job Object).

## Desenvolvimento

```bash
go test ./...
go build ./cmd/...
worker-fdc.exe -config caminho/config.json -testar
npx deno test supabase/functions/fdc/       # parâmetros da Edge Function
cd supabase/tests && npm install && npm test  # migração e fdc.q_* num Postgres em memória
```
