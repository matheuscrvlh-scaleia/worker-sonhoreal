// Consultas que a IA do WhatsApp pode fazer. Cada uma chama uma função fdc.q_* da migração.
//
// GET /q lista tudo isto (menos args e tabelas): dá para gerar as tools da IA direto dela.
// args: valores passados à função, na ordem, com o tipo do Postgres. Os nomes são os que
//       params.converter gera (palavras gera {nome} e {nome}_termos).
// tabelas: de onde vêm os dados; a resposta traz em atualizado_em a sincronização mais
//          antiga entre elas.
//          promocao (vazia no ERP) e loja (cadastro que quase não muda) ficam de fora: paradas,
//          faziam preço e estoque em dia parecerem desatualizados (queries-views.yaml não as copia).

import type { Param } from "./params.ts";

export interface Consulta {
  nome: string;
  descricao: string;
  params: Param[];
  funcao: string;
  args: [string, "text" | "integer" | "numeric"][];
  tabelas: string[];
  max_linhas?: number;
}

const filial: Param = {
  nome: "filial",
  tipo: "texto",
  padrao: "01",
  descricao: "código da loja (consulta lojas). Sem informar, usa a loja de Bom Jardim.",
  exemplo: "01",
};
const codigo: Param = { nome: "codigo", tipo: "inteiro", obrigatorio: true, exemplo: "14386" };
const documento: Param = { nome: "documento", tipo: "documento", obrigatorio: true, exemplo: "123.456.789-09" };

const lista: Consulta[] = [
  // ============================================================ PRODUTOS
  {
    nome: "buscar_produto",
    descricao:
      `"Quanto custa X?" / "Vocês têm X?". Procura produtos ativos e com preço pelo nome (termos em ` +
      `qualquer ordem, procurando também na marca/fabricante e no grupo), pelo código interno, pelo EAN ` +
      `ou pela referência de fábrica. Devolve preço à vista, a prazo, o promocional (se houver promoção ` +
      `vigente), o estoque na loja, o estoque somado de todas as lojas e do depósito, e o link da foto ` +
      `principal (foto_url, null se não houver). Mande termos curtos e no singular: "colchao casal", ` +
      `"guarda roupa 6 portas", "moto g06". estoque_loja > 0 = pronta entrega na loja; só ` +
      `estoque_total > 0 = tem no depósito/outra loja.`,
    max_linhas: 15,
    params: [
      { nome: "busca", tipo: "palavras", obrigatorio: true, descricao: "termos do produto, código, EAN ou referência", exemplo: "colchao casal" },
      filial,
    ],
    funcao: "q_buscar_produto",
    args: [["busca", "text"], ["busca_termos", "text"], ["filial", "text"]],
    tabelas: ["produto", "estoque"],
  },
  {
    nome: "detalhe_produto",
    descricao:
      `Ficha completa de UM produto (use o codigo que veio do buscar_produto): preços, promoção com ` +
      `validade, estoque, medidas, garantia, descrição do site, dados técnicos, se tem montagem e frete ` +
      `grátis, se aceita desconto, a foto principal (foto_url) e quantas fotos existem (fotos; para ` +
      `todas, use fotos_produto). ativo = N: produto fora de linha, não ofereça. Nenhuma linha: o produto ` +
      `não existe ou foi desativado.`,
    params: [codigo, filial],
    funcao: "q_detalhe_produto",
    args: [["codigo", "integer"], ["filial", "text"]],
    tabelas: ["produto", "estoque"],
  },
  {
    nome: "fotos_produto",
    descricao:
      `"Tem mais fotos?" Links de todas as fotos de UM produto, na ordem do cadastro. Cada url abre a ` +
      `imagem direto (JPEG/PNG), pronta para enviar no WhatsApp.`,
    max_linhas: 20,
    params: [codigo],
    funcao: "q_fotos_produto",
    args: [["codigo", "integer"]],
    tabelas: ["foto"],
  },
  {
    nome: "estoque_por_loja",
    descricao:
      `Em quais lojas (e no depósito) tem o produto e quantas unidades em cada uma. Use quando não ` +
      `tiver na loja do cliente, para oferecer outra loja ou entrega a partir do depósito.`,
    params: [codigo],
    funcao: "q_estoque_por_loja",
    args: [["codigo", "integer"]],
    tabelas: ["estoque"],
  },
  {
    nome: "promocoes_vigentes",
    descricao:
      `"Tem alguma promoção?" Produtos em promoção hoje, do maior desconto para o menor. ` +
      `Opcionalmente filtra por termos ("colchao").`,
    max_linhas: 30,
    params: [filial, { nome: "busca", tipo: "palavras", max_palavras: 3, descricao: "opcional, termos para filtrar", exemplo: "" }],
    funcao: "q_promocoes_vigentes",
    args: [["filial", "text"], ["busca_termos", "text"]],
    tabelas: ["produto", "estoque"],
  },

  // ============================================================ PAGAMENTO E NEGOCIAÇÃO
  {
    nome: "condicoes_pagamento",
    descricao:
      `"Parcela em quantas vezes?" Condições de pagamento ativas: número de parcelas, se tem entrada, ` +
      `fator de acréscimo (1 = sem juros), parcela mínima e validade.`,
    params: [filial],
    funcao: "q_condicoes_pagamento",
    args: [["filial", "text"]],
    tabelas: ["condicao", "condicao_fator"],
  },
  {
    nome: "simular_parcelamento",
    descricao:
      `"Fica quanto em 10x?" Simula um valor em todas as condições de pagamento ativas: total com o fator ` +
      `da condição e valor de cada parcela. Já remove as condições em que a parcela ficaria abaixo da ` +
      `mínima. Passe em valor o preço do produto (preco_aprazo para condições a prazo). É uma ESTIMATIVA: ` +
      `o valor final é o que o vendedor fechar no sistema.`,
    params: [{ nome: "valor", tipo: "numero", obrigatorio: true, exemplo: "1999.90" }, filial],
    funcao: "q_simular_parcelamento",
    args: [["valor", "numeric"], ["filial", "text"]],
    tabelas: ["condicao", "condicao_fator"],
  },
  {
    nome: "negociar_preco",
    descricao:
      `"Faz por R$ X?" / "Dá pra fazer tal valor?". Diz se o valor pedido pelo cliente cabe no desconto ` +
      `permitido para o produto (à vista). Produto em promoção ou marcado "sem desconto" não tem desconto ` +
      `extra. Responda só "consigo" / "não consigo, o melhor é R$ preco_minimo". NUNCA diga ao cliente ` +
      `qual é o desconto máximo nem que existe um limite.`,
    params: [
      codigo,
      { nome: "valor_pedido", tipo: "numero", obrigatorio: true, descricao: "valor que o cliente quer pagar", exemplo: "1800" },
      filial,
      // % usado quando o grupo do produto não define PERCDESCONTO. Combine com a loja.
      { nome: "desconto_maximo_loja", tipo: "numero", fixo: true, padrao: "5" },
    ],
    funcao: "q_negociar_preco",
    args: [["codigo", "integer"], ["valor_pedido", "numeric"], ["filial", "text"], ["desconto_maximo_loja", "numeric"]],
    tabelas: ["produto"],
  },

  // ============================================================ CLIENTE
  //
  // Telefone identifica, mas não autentica (número troca de dono, celular da família...).
  // Para qualquer dado financeiro ou de compra, pedir o CPF e usar as consultas por documento.
  {
    nome: "cliente_por_telefone",
    descricao:
      `"Já sou cliente?" Procura o cadastro pelo telefone do WhatsApp (compara os últimos 8 dígitos com ` +
      `celular, telefones e telefone alternativo). Pode voltar mais de um cadastro (família usando o mesmo ` +
      `número). Mostra só os 4 últimos dígitos do CPF/CNPJ; para dados financeiros peça o CPF completo e ` +
      `use cliente_por_documento.`,
    max_linhas: 5,
    params: [{
      nome: "telefone",
      tipo: "telefone",
      obrigatorio: true,
      descricao: "número do WhatsApp, em qualquer formato (5522987654321, (22) 98765-4321...)",
      exemplo: "5522987654321",
    }],
    funcao: "q_cliente_por_telefone",
    args: [["telefone", "text"]],
    tabelas: ["cliente"],
  },
  {
    nome: "cliente_por_documento",
    descricao:
      `Cadastro do cliente pelo CPF/CNPJ (com ou sem pontuação): nome, contato, endereço, cliente desde, ` +
      `última compra, limite de crédito, situação, quanto deve e quantas parcelas estão atrasadas. ` +
      `Nenhuma linha = não é cliente.`,
    max_linhas: 1,
    params: [documento],
    funcao: "q_cliente_por_documento",
    args: [["documento", "text"]],
    tabelas: ["cliente", "parcela"],
  },
  {
    nome: "parcelas_em_aberto",
    descricao:
      `"Quanto eu devo?" / "Manda meu boleto". Parcelas em aberto do cliente pelo CPF/CNPJ, da mais antiga ` +
      `para a mais nova, com dias de atraso, PIX copia e cola e código de barras quando o ERP tiver gerado. ` +
      `Valores sem juros/multa de atraso: para o valor atualizado, oriente o cliente a falar com a loja.`,
    max_linhas: 36,
    params: [documento],
    funcao: "q_parcelas_em_aberto",
    args: [["documento", "text"]],
    tabelas: ["parcela"],
  },
  {
    nome: "compras_cliente",
    descricao:
      `"O que eu comprei?" / "Cadê meu pedido?" / "Quando entrega?" / "Quando vem o montador?". Últimas 10 ` +
      `compras do cliente (não canceladas, dos últimos 24 meses) com itens, valor, nota fiscal, se é ` +
      `entrega em casa e as datas de entrega e montagem registradas no ERP. Em itens, "(entregue)" marca o ` +
      `que já foi entregue; itens_pendentes = quantos ainda faltam entregar.`,
    max_linhas: 10,
    params: [documento],
    funcao: "q_compras_cliente",
    args: [["documento", "text"]],
    tabelas: ["compra"],
  },

  // ============================================================ LOJA
  {
    nome: "lojas",
    descricao:
      `"Onde fica a loja?" Lojas com endereço, telefone e e-mail. O campo filial é o código usado nas ` +
      `outras consultas.`,
    params: [],
    funcao: "q_lojas",
    args: [],
    tabelas: ["loja"],
  },
];

export const CONSULTAS = new Map(lista.map((c) => [c.nome, c]));
