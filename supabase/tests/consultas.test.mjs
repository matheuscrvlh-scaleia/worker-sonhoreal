// Roda a migração num Postgres em memória (PGlite) e exercita fdc.aplicar e as fdc.q_*.
//   cd supabase/tests && npm install && npm test
import { PGlite } from "@electric-sql/pglite";
import { pg_trgm } from "@electric-sql/pglite/contrib/pg_trgm";
import { unaccent } from "@electric-sql/pglite/contrib/unaccent";
import fs from "node:fs";

const db = new PGlite({ extensions: { pg_trgm, unaccent } });
const migracao = fs.readFileSync(new URL("../migrations/20261027120000_fdc.sql", import.meta.url), "utf8");

// O mínimo do Supabase que a migração usa.
await db.exec(`
  create role anon; create role authenticated;
  create schema extensions; create schema storage;
  create table storage.buckets (id text primary key, name text, public boolean, file_size_limit bigint, allowed_mime_types text[]);
`);
await db.exec(migracao);
console.log("migração OK");

const hoje = (await db.query(`select fdc.hoje()::text as d`)).rows[0].d;
const somar = (dias) => (new Date(Date.parse(hoje) + dias * 864e5)).toISOString().slice(0, 10);

async function aplicar(tabela, linhas, remover = []) {
  linhas = linhas.map((l, i) => ({ hash: "h" + i, ...l }));
  await db.query(`select fdc.aplicar($1, $2::jsonb, array(select jsonb_array_elements_text($3::jsonb)))`,
    [tabela, JSON.stringify(linhas), JSON.stringify(remover)]);
}
async function q(sql, args = []) {
  return (await db.query(`select r from ${sql} r`, args)).rows.map((x) => x.r);
}
let falhas = 0;
function confere(nome, ok, info) {
  console.log(`${ok ? "OK    " : "FALHOU"} ${nome}${ok ? "" : "  " + JSON.stringify(info)}`);
  if (!ok) falhas++;
}

await aplicar("produto", [
  { chave: "1", codigo: 1, descricao: "COLCHAO CASAL GAZIN DREAM", marca: "GAZIN", grupo: "COLCHOES", fora_de_linha: "N",
    preco_avista: 1199, preco_aprazo: 1299, sem_desconto: "N", desconto_grupo: 0, extra_ignorada: "x" },
  { chave: "2", codigo: 2, descricao: "COLCHÃO CASAL ORTOBOM", marca: "ORTOBOM", grupo: "COLCHOES", fora_de_linha: "N",
    preco_avista: "1000.50", preco_aprazo: 1100, promocao_preco: 900, promocao_inicio: somar(-1), promocao_fim: somar(5) },
  { chave: "3", codigo: 3, descricao: "GUARDA ROUPA 6 PORTAS", grupo: "MOVEIS", fora_de_linha: "N", preco_avista: 999,
    promocao_preco: 800, promocao_inicio: somar(-10), promocao_fim: somar(-1) },
]);
await aplicar("estoque", [
  { chave: "1|01", codigo: 1, filial: "01", estoque: 0.5 }, { chave: "2|01", codigo: 2, filial: "01", estoque: 3 },
  { chave: "2|CD", codigo: 2, filial: "CD", estoque: 10 },
]);
await aplicar("loja", [
  { chave: "01", filial: "01", nome: "SONHO REAL BOM JARDIM", endereco: "RUA A", ordem: 1 },
  { chave: "CD", filial: "CD", nome: "DEPOSITO", endereco: null },
]);
await aplicar("condicao", [
  { chave: "10", codigo: 10, descricao: "10X SEM JUROS", parcelas: 10, com_entrada: "N", fator: 1, parcela_minima: 50 },
  { chave: "12", codigo: 12, descricao: "12X", parcelas: 12, com_entrada: "N", fator: 1.1, parcela_minima: 200 },
  { chave: "99", codigo: 99, descricao: "VENCIDA", parcelas: 2, fator: 1, fim: somar(-1) },
]);
await aplicar("condicao_fator", [{ chave: "10|01", codigo: 10, filial: "01", fator: 1.05 }]);
await aplicar("cliente", [
  { chave: "12345678909", documento: "12345678909", nome: "MARIA", celular: "(22) 99203-3515", telefones: "2235551234 / 22 3555-9999",
    ultima_compra: somar(-30), ativo: "S" },
  { chave: "111.222.333-44", documento: "111.222.333-44", nome: "JOSE", telefones_extra: "22992033515|2199998888", ativo: "S" },
]);
await aplicar("parcela", [
  { chave: "01|A1", filial: "01", titulo: "A1", parcela: 1, documento: "12345678909", vencimento: somar(-3), valor: 100 },
  { chave: "01|A2", filial: "01", titulo: "A2", parcela: 2, documento: "12345678909", vencimento: somar(27), valor: 100 },
]);
await aplicar("compra", [
  { chave: "01|500", filial: "01", venda: 500, documento: "123.456.789-09", data: somar(-30), total: 1199, nota_fiscal: 123,
    itens: "1x COLCHAO", itens_pendentes: 1 },
]);
await db.query(`insert into fdc.foto (chave, hash, codigo, seq, url) values ('2|1','x',2,1,'https://f/2/1.jpg'), ('2|2','y',2,2,'https://f/2/2.jpg')`);

// buscar: termos, acento no ERP, código, ordem (estoque na loja primeiro), fora de linha
let r = await q(`fdc.q_buscar_produto($1, $2, $3)`, ["COLCHAO CASAL", "COLCHAO CASAL", "01"]);
confere("buscar: acha os dois colchões (um com acento no ERP)", r.length === 2, r);
confere("buscar: maior estoque na loja primeiro, foto e promo", r[0]?.codigo === 2 && r[0].foto_url === "https://f/2/1.jpg" && r[0].preco_promocional === 900 && r[0].estoque_total === 13, r[0]);
confere("buscar: numeric vira número no JSON", typeof r[0]?.preco_avista === "number", r[0]);
r = await q(`fdc.q_buscar_produto($1, $2, $3)`, ["3", "", "01"]);
confere("buscar: por código", r.length === 1 && r[0].codigo === 3 && r[0].preco_promocional === null, r);
r = await q(`fdc.q_buscar_produto($1, $2, $3)`, ["GAZIN", "GAZIN", "01"]);
confere("buscar: pela marca", r.length === 1 && r[0].codigo === 1, r);
r = await q(`fdc.q_buscar_produto($1, $2, $3)`, ["", "", "01"]);
confere("buscar: sem termos não devolve o catálogo inteiro", r.length === 0, r);

r = await q(`fdc.q_detalhe_produto($1, $2)`, [2, "01"]);
confere("detalhe: promoção até, fotos, ativo", r[0]?.promocao_ate === somar(5) && r[0].fotos === 2 && r[0].ativo === "S", r[0]);
r = await q(`fdc.q_fotos_produto($1)`, [2]);
confere("fotos: na ordem", r.map((x) => x.seq).join() === "1,2", r);
r = await q(`fdc.q_estoque_por_loja($1)`, [2]);
confere("estoque_por_loja: maior primeiro, com nome", r[0]?.filial === "CD" && r[0].loja === "DEPOSITO" && r.length === 2, r);
r = await q(`fdc.q_promocoes_vigentes($1, $2)`, ["01", ""]);
confere("promoções: só a vigente (a vencida ontem não)", r.length === 1 && r[0].codigo === 2 && r[0].desconto_perc === 10, r);
r = await q(`fdc.q_promocoes_vigentes($1, $2)`, ["01", "GUARDA"]);
confere("promoções: filtro por termo", r.length === 0, r);

r = await q(`fdc.q_condicoes_pagamento($1)`, ["01"]);
confere("condições: sem a vencida, fator da loja", r.length === 2 && r[0].fator === 1.05, r);
r = await q(`fdc.q_simular_parcelamento($1, $2)`, [1000, "01"]);
confere("simular: tira a que fica abaixo da parcela mínima", r.length === 1 && r[0].valor_parcela === 105 && r[0].total === 1050, r);
r = await q(`fdc.q_negociar_preco($1, $2, $3, $4)`, [1, 1150, "01", 5]);
confere("negociar: 1150 de 1199 com 5% cabe", r[0]?.pode_aceitar === "S" && r[0].preco_minimo === 1139.05, r[0]);
r = await q(`fdc.q_negociar_preco($1, $2, $3, $4)`, [2, 850, "01", 5]);
confere("negociar: em promoção não tem desconto extra", r[0]?.pode_aceitar === "N" && r[0].preco_minimo === 900, r[0]);

r = await q(`fdc.q_cliente_por_telefone($1)`, ["92033515"]);
confere("telefone: acha os dois (celular e tabela TELEFONES)", r.length === 2 && r[0].nome === "MARIA" && r[0].documento_final4 === "8909", r);
r = await q(`fdc.q_cliente_por_telefone($1)`, ["35559999"]);
confere("telefone: segundo número do campo telefones", r.length === 1, r);
r = await q(`fdc.q_cliente_por_telefone($1)`, ["15552235"]);
confere("telefone: não junta pedaços de números diferentes", r.length === 0, r);
r = await q(`fdc.q_cliente_por_documento($1)`, ["12345678909"]);
confere("documento: saldo e atrasadas", r[0]?.saldo_devedor === 200 && r[0].parcelas_atrasadas === 1, r[0]);
r = await q(`fdc.q_cliente_por_documento($1)`, ["11122233344"]);
confere("documento: CPF gravado com máscara", r.length === 1 && r[0].nome === "JOSE", r);
r = await q(`fdc.q_parcelas_em_aberto($1)`, ["12345678909"]);
confere("parcelas: dias de atraso", r[0]?.dias_atraso === 3 && r[1].dias_atraso === 0, r);
r = await q(`fdc.q_compras_cliente($1)`, ["12345678909"]);
confere("compras: por documento com máscara no ERP", r.length === 1 && r[0].nota_fiscal === "123", r);
r = await q(`fdc.q_lojas()`);
confere("lojas: só com endereço", r.length === 1 && r[0].filial === "01", r);

// atualizar e apagar
await aplicar("estoque", [{ chave: "1|01", codigo: 1, filial: "01", estoque: 7 }], ["2|CD"]);
r = (await db.query(`select chave, estoque::float8 as e from fdc.estoque order by chave`)).rows;
confere("aplicar: atualiza pela chave e apaga", JSON.stringify(r) === JSON.stringify([{ chave: "1|01", e: 7 }, { chave: "2|01", e: 3 }]), r);
let erro = null;
try { await aplicar("worker_status", [{ chave: "x" }]); } catch (e) { erro = e.message; }
confere("aplicar: recusa tabela que não é de cópia", /não é sincronizada/.test(erro ?? ""), erro);

console.log(falhas ? `\n${falhas} falha(s)` : "\nTudo certo.");
process.exit(falhas ? 1 : 0);
