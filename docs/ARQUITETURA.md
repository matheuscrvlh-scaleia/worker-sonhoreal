# Arquitetura do worker-fdc

## Antes e depois

**Antes (até v1):** a IA consultava o ERP ao vivo, por um túnel Cloudflare até a API no PC da
loja. PC desligado, sem internet ou Firebird parado = IA sem resposta.

```
IA ──HTTPS──► Cloudflare Tunnel ──► ponte-api (PC da loja) ──► Firebird
```

**Agora:** o worker copia o ERP para o Supabase de tempos em tempos e a IA consulta só o Supabase.
PC desligado = a IA continua respondendo com a última cópia, e cada resposta diz de quando ela é.

```
                 PC da loja                                      Supabase (fdc-sonho-real)
┌───────────────────────────────────────────┐          ┌──────────────────────────────────────┐
│ Firebird ◄── worker-fdc.exe               │  HTTPS   │ Edge Function "fdc"                  │
│   (só lê)    · lê cada extração            ├─────────►│  /sync/*  grava (token do worker)    │
│              · compara hash por linha     │  só 443  │  /q/*     consulta (token da IA) ◄───┼── IA / n8n
│              · manda só o que mudou       │  de saída│  /admin/* diagnóstico (token admin)  │
│              ▲                            │          │       │                              │
│ worker-fdc-agente.exe (serviço WorkerFDC) │          │       ▼                              │
│   mantém no ar + atualiza sozinho ◄───────┼── R2 ────│ Postgres: schema fdc (não exposto)   │
└───────────────────────────────────────────┘          │ Storage: bucket fotos (público)      │
                                                       └──────────────────────────────────────┘
```

## Peças

| Onde | O que | Linguagem |
|---|---|---|
| `cmd/worker-fdc` | Lê o Firebird e envia para o Supabase. `-testar`, `-uma-vez`. `/health` só em 127.0.0.1. | Go |
| `cmd/worker-fdc-agente` | Serviço do Windows `WorkerFDC`: mantém o worker no ar e aplica versões novas com rollback. | Go |
| `internal/replica` | Diferença por hash, lotes, fotos, comandos da equipe, sinal de vida. | Go |
| `queries.yaml` | **O que é copiado** (`sincronizar`) e os diagnósticos (`consultas`). Atualiza sozinho. | SQL Firebird |
| `supabase/migrations` | Tabelas `fdc.*`, `fdc.aplicar()` (gravação) e `fdc.q_*()` (perguntas da IA). | SQL Postgres |
| `supabase/functions/fdc` | Edge Function: tokens, parâmetros, rotas `/q`, `/sync`, `/admin`, upload de fotos. | TypeScript (Deno) |

## Como a cópia funciona

1. A cada `intervalo_minutos`, o worker roda o SQL da extração inteiro (é leitura, com timeout).
2. Para cada linha, monta a `chave` (colunas da chave do ERP, ex.: `14386|01`) e um `hash` do
   conteúdo.
3. Compara com o estado local (`C:\worker-fdc\estado\<tabela>.json`) e envia só as linhas novas ou
   alteradas, em lotes de 500, mais as chaves que sumiram do ERP (para apagar).
4. Cada lote confirmado vai para o estado local. Se cair no meio, o próximo ciclo continua dali.
5. Uma vez por dia (ou se o estado local sumir), o worker compara com os hashes que **estão no
   Supabase** em vez do arquivo local. Isso corrige qualquer diferença.
6. **Trava de segurança:** se a extração voltar vazia, ou se fosse apagar mais que
   `max_remocao_perc` (padrão 30%) do que já está lá, o worker não envia nada e registra o erro.
   Isso protege contra SQL quebrado ou contra apontar, sem querer, para um backup antigo.
7. Fotos: a mesma comparação, por tamanho e data de alteração (`DA`). Foto nova ou trocada é lida
   do ERP, reduzida se passar de 1 MB e enviada para o Storage. A URL leva o hash, então muda
   quando a foto muda.

As regras que dependem da data (promoção vigente, condição de pagamento válida, dias de atraso)
são calculadas **no Supabase, na hora da consulta** (`fdc.hoje()`, horário de São Paulo). Com
o PC desligado, uma promoção que acabou ontem não continua aparecendo.

## Segurança

- O PC da loja só guarda o `FDC_TOKEN_WORKER`, que só grava nas tabelas de cópia, pela Edge
  Function. **A service role key do Supabase não vai para a loja.**
- O schema `fdc` não é exposto na API REST do Supabase: ninguém lê as tabelas com a anon key.
  Só a Edge Function, com conexão direta, enxerga os dados.
- Os tokens são comparados em tempo constante. O da IA não grava nada, e o do worker não consulta.
- Dados pessoais (CPF, telefone, endereço, dívidas) passam a ficar também no Supabase (região
  sa-east-1). Isso é necessário para a IA responder sem o PC. Ninguém além da Edge Function tem
  acesso a eles.
- Nada de SQL livre: o diagnóstico remoto só roda as consultas nomeadas do `queries.yaml`.
- O worker só abre conexão **de saída** em HTTPS (443). A loja não tem mais porta nem túnel aberto.

## O que saiu

| Removido | Por quê |
|---|---|
| `internal/api` (API HTTP pública, `/q`, `/foto`, `/admin/sql`) | a IA consulta o Supabase |
| Túnel Cloudflare (`cloudflared`, `sonhoreal-erp.scaleia.ai`) | nada mais entra na loja. `SECRET_TUNNEL` do `.env` pode ser apagado. |
| `token` / `token_admin` no `config.json` da loja | os tokens agora são secrets da Edge Function |
| `sql_livre` | SQL livre remoto era risco. Diagnóstico agora é por consulta nomeada. |
| Consultas da IA no `queries.yaml` | viraram funções `fdc.q_*` no Postgres |
| `powercfg` no instalador | o PC não precisa mais ficar ligado o tempo todo |

## Deploy

**Supabase (uma vez, e a cada mudança em `supabase/`):**
```bash
export SUPABASE_ACCESS_TOKEN=<SUPABASE_TOKEN do .env>
npx supabase link --project-ref mnhtuzsmwacvsymtbyck
npx supabase db push                                # aplica supabase/migrations
npx supabase secrets set FDC_TOKEN_IA=<gerar> FDC_TOKEN_ADMIN=<gerar> FDC_TOKEN_WORKER=<do instalador>
npx supabase functions deploy fdc --no-verify-jwt
```
Gerar token: `openssl rand -hex 24`.

**Loja (uma vez):** extrair o zip da release e rodar `instalar.ps1` como Administrador. Na
primeira vez, ele cria `C:\worker-fdc\config.json`, aproveita o banco de `C:\ponte` e mostra o
token do worker para cadastrar no Supabase. Na segunda vez, testa, remove o `PonteERP` e o
`cloudflared` antigos e instala o serviço `WorkerFDC`.

**Versões novas do worker / queries.yaml:** `git tag vX.Y.Z && git push --tags`, como antes. O
manifesto agora fica em `fdc/<cliente>/manifest.json`. A instalação antiga não enxerga essas
versões, então nada muda na loja antes da migração.

## Por que o worker continua em Go (e não Node)

O TypeScript já entrou onde faz sentido: a Edge Function do Supabase roda em Deno, e quem
mexe nela escreve TS. Para o worker, que roda no PC da loja, manter Go é melhor:

- **Instalação:** um `.exe` estático de ~10 MB, sem runtime. Em Node seria preciso instalar o
  Node no PC da loja (e mantê-lo atualizado) ou empacotar um binário de 60–100 MB.
- **Firebird:** o driver Go já foi testado neste banco (Firebird 3, charset NONE/WIN1252, BLOBs,
  cancelamento de consulta). O driver de Node (`node-firebird`) é menos mantido, e essas mesmas
  peculiaridades teriam de ser descobertas de novo.
- **O que já funciona:** serviço do Windows, job object, atualização assinada com rollback e
  `-testar` estão prontos e testados. Reescrever não traz nada que o usuário perceba.
- **Tamanho:** o que é regra de negócio (as perguntas da IA) saiu do worker e foi para o
  Postgres. O worker agora é simples e muda pouco: quase toda evolução é SQL no `queries.yaml` e
  nas `fdc.q_*`.

Node passa a valer a pena se o worker precisar de algo que só exista no ecossistema JS, ou se a
equipe não tiver ninguém para manter Go. Nenhum dos dois é o caso hoje.
