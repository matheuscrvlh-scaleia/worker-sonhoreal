# Integração com o worker-fdc

O **worker-fdc** mantém no Supabase uma cópia do FDC, o ERP (Firebird) da Sonho Real Móveis.
A IA do WhatsApp consulta essa cópia para responder perguntas como "quanto custa?", "tem?",
"parcela em quantas vezes?", "já sou cliente?" e "quanto devo?".

```
ERP (Firebird, na loja) ──► worker-fdc (PC da loja) ──HTTPS──► Supabase ◄──HTTPS── IA
                             copia a cada 5–60 min              cópia + API
```

- **Somente leitura:** nenhuma chamada altera dados no ERP.
- **Não depende do PC da loja:** se ele desligar, a IA continua respondendo com a última cópia.
  Toda resposta diz de quando são os dados (`atualizado_em`, seção 3).
- O tempo de cópia de cada tipo de dado está na seção 8.

---

## 1. Acesso

| | |
|---|---|
| **URL base** | `https://mnhtuzsmwacvsymtbyck.supabase.co/functions/v1/fdc` |
| **Autenticação** | usuário e senha → `POST /auth/token` → header `Authorization: Bearer <access_token>` |
| **Formato** | JSON (UTF-8) |

### Pegar o token

```bash
BASE=https://mnhtuzsmwacvsymtbyck.supabase.co/functions/v1/fdc
curl -X POST "$BASE/auth/token" -H "Content-Type: application/json" \
  -d '{"usuario": "ia", "senha": "<senha>"}'
```
```json
{
  "access_token": "eyJhbGciOiJFUzI1NiIs...",
  "token_type": "bearer",
  "expires_in": 3600,
  "expires_at": 1793120000,
  "refresh_token": "v2x7...",
  "usuario": "ia",
  "papel": "ia"
}
```

- O `access_token` vale **1 hora** (`expires_at` em segundos Unix). Guarde e reaproveite em
  todas as chamadas, sem fazer login a cada pergunta.
- **Renovar** antes de vencer, sem a senha:
  `POST /auth/token` com `{"grant_type": "refresh_token", "refresh_token": "<refresh_token>"}`.
  Cada renovação devolve um `refresh_token` novo; use sempre o último.
- Recebeu **401** numa consulta? O token venceu ou foi revogado: peça outro (renovação ou login)
  e repita a chamada uma vez.
- Login errado devolve 401. Muitas tentativas seguidas devolvem 429: espere alguns minutos.

O token vai **sempre no header**, nunca no body nem na URL. As rotas sem token são
`POST /auth/token` e `GET /status` (seção 6). As fotos são links públicos do Storage (seção 4.5).

### Usuários

Os usuários ficam no Supabase Auth do projeto `fdc-sonho-real` (senha guardada com hash). Não
existe cadastro aberto: só um admin cria usuário (seção 6).

| Papel | Pode |
|---|---|
| `ia` | todas as consultas da seção 4 e o `/status` detalhado |
| `admin` | o mesmo, mais diagnóstico do ERP e gestão de usuários (seção 6). **Nunca configure na IA.** |

O worker da loja não usa usuário: ele tem um token próprio (`FDC_TOKEN_WORKER`), que só serve
para enviar dados e não faz consultas.

---

## 2. Como chamar

Toda consulta fica em `/q/{nome}`. Os parâmetros podem ir de dois jeitos, com o mesmo resultado:

**GET com parâmetros na URL**
```bash
curl "$BASE/q/buscar_produto?busca=colchao%20casal" \
  -H "Authorization: Bearer $TOKEN"
```

**POST com parâmetros no body**
```bash
curl -X POST "$BASE/q/buscar_produto" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"busca": "colchao casal"}'
```

> Num GET, o body é ignorado. Para mandar parâmetros no body, use POST com
> `Content-Type: application/json`.

**Parâmetros**
- Nomes exatamente como na tabela de cada consulta (minúsculos, com `_`).
- Valores podem ir como texto ou número: `{"codigo": 10110}` e `{"codigo": "10110"}` dão no mesmo.
- O que não for enviado usa o padrão. Parâmetro desconhecido é ignorado.

### Cliente HTTP da IA (o que implementar)

1. Ao iniciar, `POST /auth/token` com usuário e senha. Guarde `access_token`, `refresh_token`
   e `expires_at`.
2. Em cada pergunta, `POST /q/{nome}` com `Authorization: Bearer <access_token>`.
3. Faltando menos de 5 minutos para `expires_at`, renove com o `refresh_token`.
4. Se vier 401, renove (ou, se a renovação falhar, faça login de novo) e repita a chamada uma
   única vez.

---

## 3. Respostas

### Sucesso: HTTP 200

```json
{
  "consulta": "buscar_produto",
  "total": 2,
  "truncado": false,
  "ms": 41,
  "atualizado_em": "2026-10-27T13:05:12.418Z",
  "linhas": [
    { "codigo": 14386, "descricao": "COLCHAO CASAL GAZIN DREAM D45 138X20 PTO", "preco_avista": 1199, "...": "..." },
    { "codigo": 13430, "descricao": "COLCHAO CASAL GAZIN NEW YORK 138X26 PTO", "preco_avista": 1100, "...": "..." }
  ]
}
```

| Campo | Significado |
|---|---|
| `linhas` | resultado. **Lista vazia não é erro**: quer dizer que não achou nada. |
| `total` | quantidade de linhas devolvidas |
| `truncado` | `true` se havia mais linhas que o limite da consulta |
| `ms` | tempo da consulta no Supabase |
| `atualizado_em` | quando a cópia mais antiga usada por esta consulta foi atualizada pelo worker (UTC). `null` = ainda não houve a primeira cópia. |

**Dados desatualizados.** A IA fala como atendente da loja: **nunca** menciona "dados",
"sistema", "cópia" ou "desatualizado" para o cliente. Se `atualizado_em` tiver mais de
**2 horas** (o servidor da loja está desligado ou sem internet), continua respondendo
normalmente e, só ao fechar preço ou reservar, diz como um vendedor diria ("vou confirmar a
disponibilidade aqui na loja e já te retorno") e passa para a equipe. Para dívida e boleto, o
mesmo: confirma o valor com a equipe antes de enviar (um pagamento recente pode ainda não ter
chegado na cópia).

**Convenções dos valores**
- Datas: `"AAAA-MM-DD"`. Data e hora: ISO 8601 em UTC.
- Valores em reais: número com ponto decimal (`1199.9`), sem formatação.
- Sim/não: `"S"` / `"N"`.
- Campo sem informação no ERP: `null`.
- Textos do ERP vêm em MAIÚSCULAS e sem acento (`"COLCHAO CASAL"`). Reescreva para o cliente.

### Erros

Todo erro tem o formato `{"erro": "mensagem"}`.

| HTTP | Quando | O que a IA faz |
|---|---|---|
| 400 | parâmetro obrigatório faltando ou inválido (CPF com tamanho errado, número inválido) | pedir o dado de novo ao cliente |
| 401 | token vencido, inválido ou ausente (no `/auth/token`: usuário ou senha errados) | renovar o token ou fazer login e repetir uma vez (seção 2); se o login falhar, alertar a equipe |
| 403 | usuário sem permissão para a rota (ex.: usuário `ia` em `/admin`) | erro de configuração: alertar a equipe |
| 404 | consulta não existe | conferir o nome |
| 429 | muitas tentativas de login seguidas | esperar alguns minutos; o cliente HTTP está fazendo login demais |
| 500 / 503 | erro ou indisponibilidade do Supabase | "nosso sistema está fora agora, um atendente vai te responder" e transferir |

Com o PC da loja desligado, as consultas **não dão erro**: respondem com a última cópia
(veja `atualizado_em`).

### Limites

- Cada consulta tem um teto de linhas (indicado em cada uma).
- As consultas são rápidas (dezenas de ms). Não há fila nem limite de chamadas simultâneas
  além dos limites do plano do Supabase.

---

## 4. Consultas

`filial` é opcional em todas as consultas que o aceitam. O padrão é `01`.

| `filial` | Loja |
|---|---|
| `01` | Sonho Real Móveis, Bom Jardim/RJ (principal) |
| `02` | Sonho Real Móveis, São José do Calçado/ES |
| `CD` | depósito (só aparece em estoque) |

### 4.1 Produtos

#### `buscar_produto`
"Quanto custa X?" / "Vocês têm X?". Procura produtos **ativos e com preço**.

| Parâmetro | Obrig. | Exemplo | Observação |
|---|---|---|---|
| `busca` | sim | `colchao casal` | termos em qualquer ordem; também aceita código, EAN ou referência |
| `filial` | não | `01` | loja usada para `estoque_loja` |

Até 15 linhas. Primeiro os produtos com estoque na loja, depois os com mais estoque no total.

| Campo | |
|---|---|
| `codigo` | código interno: use nas outras consultas de produto |
| `descricao`, `marca`, `grupo`, `unidade` | |
| `preco_avista`, `preco_aprazo` | preços de tabela |
| `preco_promocional` | preço em promoção hoje, ou `null` |
| `estoque_loja` | unidades na loja da `filial` (> 0 = pronta entrega) |
| `estoque_total` | unidades somadas em todas as lojas e no depósito |
| `foto_url` | link da foto principal (seção 4.5), ou `null` se o produto não tem foto |

`marca` vem do fabricante cadastrado (ex.: `GAZIN`), e a busca também procura por ele: `"colchao gazin"` funciona.

**Dicas para a IA montar a `busca`:**
- termos curtos e no singular: `"colchao casal"`, `"guarda roupa 6 portas"`, `"moto g06"`;
- palavras como "de", "com", "para" e "tem" são ignoradas;
- acento não importa;
- até 4 termos são usados; mais que isso, sobra.

#### `detalhe_produto`
Ficha completa de **um** produto.

| Parâmetro | Obrig. | Exemplo |
|---|---|---|
| `codigo` | sim | `14386` |
| `filial` | não | `01` |

Campos: os mesmos de `buscar_produto`, mais:

| Campo | |
|---|---|
| `ativo` | `N` = produto fora de linha: não ofereça. Nenhuma linha = produto não existe ou foi desativado. |
| `subgrupo`, `referencia`, `ean` | |
| `promocao_ate` | fim da promoção |
| `altura`, `largura`, `comprimento` | medidas cadastradas |
| `garantia_fabrica` | como está no cadastro |
| `aplicacao`, `descricao_site`, `dados_tecnicos`, `itens_inclusos` | textos do cadastro (podem ser `null`) |
| `tem_montagem`, `montagem_gratis`, `frete_gratis` | `S`/`N` |
| `aceita_desconto` | `N` = não negociar preço |
| `foto_url` | link da foto principal, ou `null` |
| `fotos` | quantas fotos o produto tem (todas em `fotos_produto`) |

#### `fotos_produto`
"Tem mais fotos?" Links de todas as fotos de um produto.

| Parâmetro | Obrig. | Exemplo |
|---|---|---|
| `codigo` | sim | `14818` |

Até 20 linhas, na ordem do cadastro. Campos: `seq`, `url`.

#### `estoque_por_loja`
Em quais lojas (e no depósito) tem o produto. Use quando `estoque_loja` = 0 mas `estoque_total` > 0.

| Parâmetro | Obrig. | Exemplo |
|---|---|---|
| `codigo` | sim | `14386` |

Campos: `filial`, `loja`, `cidade`, `estoque`. Só aparecem lojas com estoque > 0.

#### `promocoes_vigentes`
"Tem alguma promoção?"

| Parâmetro | Obrig. | Exemplo |
|---|---|---|
| `filial` | não | `01` |
| `busca` | não | `colchao` (até 3 termos) |

Até 30 linhas, do maior para o menor desconto. Campos: `codigo`, `descricao`, `marca`,
`preco_normal`, `preco_promocional`, `desconto_perc`, `valida_ate`, `estoque_loja`.

### 4.2 Pagamento e negociação

#### `condicoes_pagamento`
"Parcela em quantas vezes?" Lista as condições ativas.

| Parâmetro | Obrig. |
|---|---|
| `filial` | não |

| Campo | |
|---|---|
| `codigo`, `descricao` | |
| `parcelas` | número de parcelas |
| `com_entrada` | `S` = a primeira parcela é paga na compra |
| `fator` | multiplicador sobre o valor; `1` = sem juros |
| `desconto_perc`, `parcela_minima`, `entrada_minima_perc`, `valida_ate` | |

#### `simular_parcelamento`
"Fica quanto em 10x?" Aplica um valor em todas as condições ativas.

| Parâmetro | Obrig. | Exemplo | Observação |
|---|---|---|---|
| `valor` | sim | `1199` | aceita `1199.90`, `1.199,90`, `R$ 1.199` |
| `filial` | não | `01` | |

Campos: `codigo`, `descricao`, `parcelas`, `com_entrada`, `fator`, `total`, `valor_parcela`.
Condições cuja parcela ficaria abaixo da mínima já vêm removidas.

> ⚠️ É **estimativa**. A IA deve dizer "aproximadamente" ou "a partir de" e deixar claro que o
> valor final é confirmado pelo vendedor.

#### `negociar_preco`
"Faz por R$ X?" Diz se o valor pedido cabe no desconto permitido (pagamento à vista).

| Parâmetro | Obrig. | Exemplo |
|---|---|---|
| `codigo` | sim | `14386` |
| `valor_pedido` | sim | `1100` |
| `filial` | não | `01` |

| Campo | |
|---|---|
| `codigo`, `descricao` | |
| `preco_referencia` | preço base (o promocional, se houver) |
| `valor_pedido` | o que o cliente pediu |
| `pode_aceitar` | `S` / `N` |
| `preco_minimo` | **uso interno**: o menor valor aceitável |
| `desconto_pedido_perc` | **uso interno**: desconto que o cliente pediu |

> ⚠️ **Regras para a IA:**
> - `pode_aceitar = S`: "consigo fazer por R$ X à vista".
> - `pode_aceitar = N`: "por esse valor não consigo, o melhor que faço é R$ `preco_minimo`".
> - **Nunca** dizer que existe um limite de desconto nem qual é o percentual.
> - Produto em promoção ou marcado como sem desconto: `preco_minimo` = `preco_referencia`.
> - O limite padrão é configurado na API, e a IA não consegue alterá-lo.

### 4.3 Cliente

> **Privacidade:** o telefone **identifica**, mas não **autentica**: número troca de dono e
> família divide celular.
> - `cliente_por_telefone`: pode usar para cumprimentar pelo nome.
> - Dívida, compras e dados cadastrais: **só depois de o cliente informar o CPF**, usando as
>   consultas por `documento`.

#### `cliente_por_telefone`
"Já sou cliente?"

| Parâmetro | Obrig. | Exemplo | Observação |
|---|---|---|---|
| `telefone` | sim | `5522992033515` | o número do WhatsApp; aceita qualquer formato |

Compara os últimos 8 dígitos com os telefones do cadastro. Até 5 linhas, porque vários cadastros
podem ter o mesmo número.

| Campo | |
|---|---|
| `nome` | |
| `documento_final4` | últimos 4 dígitos do CPF/CNPJ, para confirmar: "seu CPF termina em 1234?" |
| `cidade`, `bairro`, `cliente_desde`, `ultima_compra` | |
| `ativo` | `N` = cadastro inativo |

#### `cliente_por_documento`
Cadastro completo e situação financeira.

| Parâmetro | Obrig. | Exemplo |
|---|---|---|
| `documento` | sim | `123.456.789-09` ou `12345678909` (CPF ou CNPJ) |

Até 1 linha. **Nenhuma linha = não é cliente.**

| Campo | |
|---|---|
| `nome`, `documento`, `email`, `celular`, `telefones` | |
| `endereco`, `numero`, `complemento`, `bairro`, `cidade`, `uf`, `cep` | |
| `cliente_desde`, `ultima_compra`, `valor_ultima_compra` | |
| `limite_credito`, `situacao`, `ativo` | |
| `saldo_devedor` | soma das parcelas em aberto (sem juros) |
| `parcelas_atrasadas` | quantidade de parcelas vencidas e não pagas |

#### `parcelas_em_aberto`
"Quanto eu devo?" / "Manda meu boleto."

| Parâmetro | Obrig. | Exemplo |
|---|---|---|
| `documento` | sim | `12345678909` |

Até 36 linhas, da mais antiga para a mais nova.

| Campo | |
|---|---|
| `filial`, `titulo`, `parcela`, `venda` | identificação |
| `emissao`, `vencimento` | |
| `valor` | valor original da parcela |
| `dias_atraso` | 0 = em dia |
| `forma` | forma de pagamento (crediário, boleto...) |
| `pix_copia_e_cola` | código PIX, quando o ERP gerou; senão `null` |
| `codigo_barras` | código de barras do boleto, quando o ERP gerou; senão `null` |

> ⚠️ Os valores **não incluem juros nem multa** de atraso. Para parcela atrasada, a IA deve
> informar o valor original e orientar o cliente a confirmar o valor atualizado com a loja.

#### `compras_cliente`
"O que eu comprei?" / "Cadê meu pedido?" / "Quando entrega?" / "Quando vem o montador?"

| Parâmetro | Obrig. | Exemplo |
|---|---|---|
| `documento` | sim | `12345678909` |

Últimas 10 compras não canceladas, dos últimos 24 meses.

| Campo | |
|---|---|
| `filial`, `venda`, `data`, `total`, `nota_fiscal` | |
| `itens` | texto: `"1x COLCHAO ... (entregue) \| 1x GUARDA ROUPA ..."`. Itens de garantia estendida não aparecem. |
| `itens_pendentes` | quantos itens ainda não foram entregues (0 = tudo entregue) |
| `entrega_domicilio` | `S`/`N` |
| `data_entrega`, `data_montagem` | quando registradas no ERP; senão `null` |

### 4.4 Loja

#### `lojas`
"Onde fica a loja?" Sem parâmetros.

Campos: `filial`, `nome`, `endereco`, `numero`, `bairro`, `cidade`, `uf`, `cep`, `telefones`, `email`.

### 4.5 Fotos dos produtos

As fotos ficam no Storage do Supabase (bucket `fotos`, público). O link é a própria imagem
(JPEG ou PNG, não JSON), **sem token**, porque o WhatsApp precisa baixar pela URL.

- Você não precisa montar a URL: ela vem pronta em `foto_url` (`buscar_produto`,
  `detalhe_produto`) e em `fotos_produto`.
- Foto acima de 1 MB é reduzida pelo worker para no máximo 1600 px, dentro do limite de imagem
  do WhatsApp.
- Quando a foto muda no ERP, o link muda junto (o nome do arquivo leva um hash). Não guarde
  links por muito tempo: peça de novo à consulta.

Para enviar no WhatsApp (Cloud API da Meta), use a URL como `link` da mensagem de imagem:
```json
{ "type": "image", "image": { "link": "https://mnhtuzsmwacvsymtbyck.supabase.co/storage/v1/object/public/fotos/14818/1-3f9a0c1b2d4e.jpg", "caption": "Guarda-roupa Milano 6 portas – R$ 999,00" } }
```

Em 26/10/2026, 1.801 dos 1.913 produtos ativos (94%) tinham foto, 3.396 fotos no total.

---

## 5. Fluxos sugeridos para a IA

**Produto e preço**
1. `buscar_produto` com os termos do cliente.
2. Muitos resultados: pergunte modelo, tamanho ou cor. Um só: `detalhe_produto`, e mande a
   foto (`foto_url`) junto com o preço. Se o cliente pedir mais fotos, use `fotos_produto`.
3. Sem estoque na loja e com `estoque_total` > 0: `estoque_por_loja` para oferecer outra loja ou
   entrega a partir do depósito.
4. "Quanto fica parcelado?": `simular_parcelamento` com `preco_aprazo`.
5. "Faz por X?": `negociar_preco`.

**Cliente**
1. Chegou mensagem: `cliente_por_telefone` com o número do WhatsApp, para cumprimentar pelo nome.
2. Pergunta sobre dívida, boleto ou compra: pedir o CPF, depois `cliente_por_documento`.
3. Em seguida, `parcelas_em_aberto` e/ou `compras_cliente`.

**Cópia antiga (`atualizado_em` com mais de 2 h)**
- Responder normalmente, mas dizer que preço, estoque e valores devem ser confirmados com a loja.

**Supabase fora (500/503)**
- Não inventar preço nem estoque. Avisar que o sistema está indisponível e transferir para um
  atendente.

---

## 6. Monitoramento e administração

### `GET /status` (sem token)

```json
{ "ok": true, "worker_visto_em": "2026-10-27T13:06:01.112Z", "worker_versao": "v2.0.0", "banco_erp_ok": true }
```

- HTTP **200** = o worker da loja deu sinal nos últimos 10 minutos.
- HTTP **503** = o worker sumiu (PC desligado, sem internet ou serviço parado). A IA continua
  funcionando com a cópia; é a equipe que precisa saber.
- `banco_erp_ok: false` = o worker está no ar, mas não alcança o Firebird.

Com Bearer (`ia` ou `admin`), o `/status` também traz `tabelas` (quando cada tabela foi copiada e
quantas linhas tem) e `erros` (o último erro de cada tabela, se houver).

Sugestão: monitorar `/status` a cada 5 minutos (UptimeRobot, por exemplo) e alertar no WhatsApp
da equipe depois de 2 falhas seguidas.

### `GET /q` (Bearer)

Lista todas as consultas disponíveis com descrição, parâmetros, padrões e exemplos. É a fonte de
verdade: se uma consulta mudar ou entrar uma nova, ela aparece aqui sem precisar atualizar este
documento. Dá para gerar as tools da IA automaticamente a partir dela.

### Usuários (só `admin`)

```bash
# lista
curl "$BASE/admin/usuarios" -H "Authorization: Bearer $ADMIN"
# cria, ou troca senha/papel de quem já existe (senha: mínimo 12 caracteres)
curl -X POST "$BASE/admin/usuarios" -H "Authorization: Bearer $ADMIN" -H "Content-Type: application/json" \
  -d '{"usuario": "ia-homologacao", "senha": "<senha forte>", "papel": "ia"}'
# remove
curl -X DELETE "$BASE/admin/usuarios/ia-homologacao" -H "Authorization: Bearer $ADMIN"
```

`usuario`: 3 a 40 caracteres (letras minúsculas, números, `.`, `-`, `_`). Ao remover um usuário ou
trocar a senha, o token que ele já tinha continua valendo até vencer (no máximo 1 hora).

### Diagnóstico do ERP (só `admin`)

O Firebird fica na loja e não é acessível de fora. Para rodar um diagnóstico, faça o pedido no
Supabase. O worker busca os pedidos a cada 30 s, executa no ERP e grava o resultado:

```bash
# pede
curl -X POST "$BASE/admin/comandos" -H "Authorization: Bearer $ADMIN" -H "Content-Type: application/json" \
  -d '{"tipo": "consulta", "consulta": "diag_view_colunas", "params": {"nome": "VIEWPRODUTOS"}}'
# → {"id": "12", ...}

# lê (concluido_em preenchido = pronto)
curl "$BASE/admin/comandos/12" -H "Authorization: Bearer $ADMIN"
```

| `consulta` | |
|---|---|
| `diag_versao` | versão do Firebird e charset |
| `diag_status`, `diag_receber`, `diag_receber_amostra`, `diag_flags`, `diag_estoque` | conferência das regras de negócio |
| `diag_views`, `diag_view_colunas` (`params.nome`) | código das views do fornecedor do ERP |
| `diag_fotos`, `diag_fotos_amostra` | o que existe de foto no banco |
| `diag_amostra_clientes`, `diag_amostra_produtos`, `diag_indices` | formato dos dados e índices |
| `pedidos_cliente`, `assistencias_cliente` (`params.documento`) | `PEDIDOVENDA` e `ASSISTENCIA` estão vazias neste ERP, por isso não são copiadas |

`{"tipo": "atualizar"}` faz o worker buscar uma versão nova na hora, sem esperar os 5 minutos.
Não existe SQL livre: só as consultas do `queries.yaml`.

---

## 7. Exemplo de tool para a IA (function calling)

Uma tool por consulta, com a descrição tirada do `GET /q`:

```json
{
  "name": "buscar_produto",
  "description": "Procura produtos da loja pelo nome, código ou EAN. Devolve preço à vista, a prazo, promocional e estoque. Use termos curtos no singular, ex.: 'colchao casal'.",
  "input_schema": {
    "type": "object",
    "properties": {
      "busca":  { "type": "string", "description": "termos do produto, código, EAN ou referência" },
      "filial": { "type": "string", "description": "01 = Bom Jardim (padrão), 02 = São José do Calçado" }
    },
    "required": ["busca"]
  }
}
```

A execução da tool é sempre a mesma: `POST /q/{name}` com os argumentos no body e o header
`Authorization`. A resposta (`linhas` e `atualizado_em`) volta para a IA como resultado.

---

## 8. Atraso da cópia e limitações

Quanto tempo uma mudança no ERP leva para aparecer para a IA (com o PC da loja ligado):

| Dado | Até |
|---|---|
| estoque | 5 min |
| produtos, preços, promoções, parcelas em aberto | 10 min |
| clientes, compras, condições de pagamento | 30 min |
| fotos, lojas | 60 min |

| Ponto | Situação |
|---|---|
| Dados | desde 10/11/2026 o worker roda no servidor do ERP e lê o banco real, mas só pelas views liberadas pela FDC (`queries-views.yaml`). **Em dia:** produtos e preço à vista, estoque, clientes e compras. **Parados em 23/10/2026** até a FDC liberar SELECT nas tabelas: parcelas em aberto, fotos, condições de pagamento, promoções e lojas. Enquanto isso, não usar `parcelas_em_aberto` para cobrança; `negociar_preco` não dá desconto (a view não diz quais produtos proíbem). |
| Pagamento recente | um título pago some da cópia em até 10 min. Até lá, ainda aparece como em aberto. |
| Produto desativado | some da cópia: `detalhe_produto` volta sem linhas. |
| Compras | só as dos últimos 24 meses. |
| Estoque | lido de `ESTOQUE.QTDER`, a mesma coluna da `VIEWESTOQUE` do fornecedor. |
| Parcelamento | o ERP marca 3x–8x e 10x–12x como inativas. Confirmar com a loja antes de confiar na simulação. |
| Juros de atraso | não calculados (`parcelas_em_aberto` mostra o valor original). |
| Títulos com status `A` | 15 títulos com significado ainda não confirmado. Hoje não aparecem como dívida. |
