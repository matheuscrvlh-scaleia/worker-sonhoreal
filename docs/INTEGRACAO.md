# Integração com o worker da Sonho Real

API HTTP que consulta o ERP (Firebird) da Sonho Real Móveis em tempo real. Ela existe para a IA
do WhatsApp responder perguntas como "quanto custa?", "tem?", "parcela em quantas vezes?", "já sou
cliente?" e "quanto devo?".

A API é **somente leitura**: nenhuma chamada altera dados no ERP.

---

## 1. Acesso

| | |
|---|---|
| **URL base** | `https://sonhoreal-erp.scaleia.ai` |
| **Autenticação** | header `Authorization: Bearer <TOKEN>` |
| **Formato** | JSON (UTF-8) |

O token vai **sempre no header**, nunca no body nem na URL. `X-Token: <TOKEN>` também é aceito.

Há dois tokens, ambos no `C:\ponte\config.json` da máquina da loja:

| Token | Uso |
|---|---|
| `token` | a IA: todas as consultas da seção 4 |
| `token_admin` | só a equipe: diagnóstico e manutenção (seção 6). **Nunca configure na IA.** |

> **Previsto:** quando o Cloudflare Access for ativado, cada requisição também vai precisar dos
> headers `CF-Access-Client-Id` e `CF-Access-Client-Secret`. Vale deixar o cliente HTTP preparado
> para headers extras.

---

## 2. Como chamar

Toda consulta fica em `/q/{nome}`. Os parâmetros podem ir de dois jeitos, com o mesmo resultado:

**GET com parâmetros na URL**
```bash
curl "https://sonhoreal-erp.scaleia.ai/q/buscar_produto?busca=colchao%20casal" \
  -H "Authorization: Bearer $TOKEN"
```

**POST com parâmetros no body**
```bash
curl -X POST "https://sonhoreal-erp.scaleia.ai/q/buscar_produto" \
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

### n8n

Nó **HTTP Request**:
- **Method:** `POST`
- **URL:** `https://sonhoreal-erp.scaleia.ai/q/{{ $json.consulta }}`, ou fixa por consulta
- **Authentication:** Generic Credential Type → **Header Auth**
  - Name: `Authorization`
  - Value: `Bearer <TOKEN>`
- **Send Body:** JSON → os parâmetros da consulta
- **Settings → Never Error:** ligado, para tratar 4xx/5xx no fluxo (seção 3)

---

## 3. Respostas

### Sucesso: HTTP 200

```json
{
  "consulta": "buscar_produto",
  "total": 2,
  "truncado": false,
  "ms": 632,
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
| `ms` | tempo da consulta no banco |

**Convenções dos valores**
- Datas: `"AAAA-MM-DD"`. Data e hora: `"AAAA-MM-DD HH:MM:SS"`.
- Valores em reais: número com ponto decimal (`1199.9`), sem formatação.
- Sim/não: `"S"` / `"N"`.
- Campo sem informação no ERP: `null`.
- Textos do ERP vêm em MAIÚSCULAS e sem acento (`"COLCHAO CASAL"`). Reescreva para o cliente.

### Erros

Todo erro tem o formato `{"erro": "mensagem"}`.

| HTTP | Quando | O que a IA faz |
|---|---|---|
| 400 | parâmetro obrigatório faltando ou inválido (CPF com tamanho errado, número inválido) | pedir o dado de novo ao cliente |
| 401 | token errado ou ausente | erro de configuração: alertar a equipe |
| 403 | consulta exige `token_admin` | não usar essa consulta |
| 404 | consulta não existe | conferir o nome |
| 503 | banco do ERP fora do ar, PC da loja desligado ou API ocupada | "nosso sistema está fora agora, um atendente vai te responder" e transferir |
| 504 | consulta passou de 15 segundos | tentar uma vez de novo; se repetir, tratar como 503 |
| 502 / 530 | erro da Cloudflare: a máquina da loja não responde | tratar como 503 |

### Limites

- Até **4 consultas simultâneas**; acima disso, a fila espera até o timeout.
- **Timeout de 15 s** por consulta.
- Cada consulta tem um teto de linhas (indicado em cada uma).

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
| `ativo` | `N` = produto fora de linha: não ofereça |
| `subgrupo`, `referencia`, `ean` | |
| `promocao_ate` | fim da promoção |
| `altura`, `largura`, `comprimento` | medidas cadastradas |
| `garantia_fabrica` | como está no cadastro |
| `aplicacao`, `descricao_site`, `dados_tecnicos`, `itens_inclusos` | textos do cadastro (podem ser `null`) |
| `tem_montagem`, `montagem_gratis`, `frete_gratis` | `S`/`N` |
| `aceita_desconto` | `N` = não negociar preço |

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

Últimas 10 compras não canceladas.

| Campo | |
|---|---|
| `filial`, `venda`, `data`, `total`, `nota_fiscal` | |
| `itens` | texto: `"1x COLCHAO ... \| 2x TRAVESSEIRO ..."` |
| `entrega_domicilio` | `S`/`N` |
| `data_entrega`, `data_montagem` | quando registradas no ERP; senão `null` |

### 4.4 Loja

#### `lojas`
"Onde fica a loja?" Sem parâmetros.

Campos: `filial`, `nome`, `endereco`, `numero`, `bairro`, `cidade`, `uf`, `cep`, `telefones`, `email`.

---

## 5. Fluxos sugeridos para a IA

**Produto e preço**
1. `buscar_produto` com os termos do cliente.
2. Muitos resultados: pergunte modelo, tamanho ou cor. Um só: `detalhe_produto`.
3. Sem estoque na loja e com `estoque_total` > 0: `estoque_por_loja` para oferecer outra loja ou
   entrega a partir do depósito.
4. "Quanto fica parcelado?": `simular_parcelamento` com `preco_aprazo`.
5. "Faz por X?": `negociar_preco`.

**Cliente**
1. Chegou mensagem: `cliente_por_telefone` com o número do WhatsApp, para cumprimentar pelo nome.
2. Pergunta sobre dívida, boleto ou compra: pedir o CPF, depois `cliente_por_documento`.
3. Em seguida, `parcelas_em_aberto` e/ou `compras_cliente`.

**Sistema fora (503/502/530)**
- Não inventar preço nem estoque. Avisar que o sistema está indisponível e transferir para um
  atendente.

---

## 6. Monitoramento e administração

### `GET /health` (sem token)

```json
{ "ok": true, "versao": "dev", "cliente": "sonho-real", "banco_ok": true, "banco_ms": 2,
  "consultas": 22, "consultas_sha": "c616fb...", "no_ar_desde": "2026-10-26T16:52:29-03:00" }
```

- HTTP **200** = API e banco ok. HTTP **503** = API no ar, mas banco fora.
- Erro de rede, 502 ou 530 = máquina da loja ou API fora do ar.
- `no_ar_desde` mudou = a API reiniciou (reboot do PC, por exemplo).

Sugestão: monitorar a cada 5 minutos (UptimeRobot, ou um fluxo no n8n) e alertar no WhatsApp da
equipe depois de 2 falhas seguidas.

### `GET /q` (token)

Lista todas as consultas disponíveis com descrição, parâmetros, padrões e exemplos. É a fonte de
verdade: se uma consulta mudar ou entrar uma nova, ela aparece aqui sem precisar atualizar este
documento. Dá para gerar as tools da IA automaticamente a partir dela.

### Só com `token_admin`

| Endpoint | |
|---|---|
| `GET /q/diag_versao` | versão do Firebird e charset |
| `GET /q/diag_status`, `diag_receber`, `diag_receber_amostra`, `diag_flags` | conferência das regras de negócio |
| `GET /q/diag_amostra_clientes`, `diag_amostra_produtos`, `diag_indices` | formato dos dados e índices |
| `GET /q/pedidos_cliente`, `assistencias_cliente` | desligadas para a IA: `PEDIDOVENDA` e `ASSISTENCIA` estão vazias neste ERP |
| `POST /admin/atualizar` | pede atualização imediata (quando a auto-atualização estiver ligada) |

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
`Authorization`. A resposta inteira (`linhas`) volta para a IA como resultado.

---

## 8. Limitações conhecidas

| Ponto | Situação |
|---|---|
| Dados | hoje a API lê uma **cópia de backup** do banco (23/10/2026) no PC da Leila. Preço, estoque e títulos não refletem o dia até ela apontar para o servidor real. |
| Estoque | todos os colchões consultados vieram com estoque 0. Ainda não se sabe se é real ou se o ERP controla saldo em outro campo. |
| Parcelamento | o ERP marca 3x–8x e 10x–12x como inativas. Confirmar com a loja antes de confiar na simulação. |
| Juros de atraso | não calculados (`parcelas_em_aberto` mostra o valor original). |
| Títulos com status `A` | 15 títulos com significado ainda não confirmado. Hoje não aparecem como dívida. |
| Disponibilidade | a API depende do PC da loja ligado. Ela volta sozinha uns 3 minutos depois de um reinício. |
