-- worker-fdc: cópia do ERP FDC (Firebird) da Sonho Real, mantida pelo worker da loja.
--
-- O worker lê o Firebird, compara com o que já mandou (hash por linha) e envia só o que mudou
-- para a Edge Function "fdc", que grava aqui com fdc.aplicar(). A IA consulta pela mesma Edge
-- Function (/q/{nome}), que chama as funções fdc.q_*. Se o PC da loja desligar, a IA continua
-- respondendo com a última cópia (cada resposta diz de quando ela é).
--
-- Toda tabela sincronizada tem três colunas de controle:
--   chave            montada pelo worker com a chave do ERP ("14386|01"); é por ela que ele
--                    atualiza e apaga
--   hash             resumo da linha no ERP; o worker compara para saber o que mudou
--   sincronizado_em  última vez que a linha foi gravada
-- O resto são as colunas que o SQL do queries.yaml devolve, com o mesmo nome.
--
-- O schema fdc não é exposto na API REST: só a Edge Function (conexão direta) enxerga.

create extension if not exists pg_trgm with schema extensions;
create extension if not exists unaccent with schema extensions;

create schema fdc;
revoke all on schema fdc from public;

-- ============================================================ APOIO

-- Data de hoje na loja (o servidor do Supabase roda em UTC).
create function fdc.hoje() returns date
language sql stable as $$ select (now() at time zone 'America/Sao_Paulo')::date $$;

-- Maiúsculas e sem acento, como o ERP grava. Imutável para poder ir em coluna gerada.
create function fdc.sem_acento(t text) returns text
language sql immutable parallel safe
as $$ select upper(extensions.unaccent('extensions.unaccent'::regdictionary, coalesce(t, ''))) $$;

create function fdc.digitos(t text) returns text
language sql immutable parallel safe
as $$ select regexp_replace(coalesce(t, ''), '\D', '', 'g') $$;

-- ============================================================ TABELAS SINCRONIZADAS

-- Só produtos ativos no ERP. Produto desativado some daqui.
create table fdc.produto (
  chave            text primary key,
  hash             text not null,
  sincronizado_em  timestamptz not null default now(),
  codigo           integer not null,
  descricao        text not null,
  fora_de_linha    text,     -- S/N
  marca            text,
  grupo            text,
  subgrupo         text,
  referencia       text,
  ean              text,
  unidade          text,
  preco_avista     numeric,
  preco_aprazo     numeric,
  promocao_preco   numeric,  -- promoção do cadastro do produto; a vigência é conferida na consulta
  promocao_inicio  date,
  promocao_fim     date,
  altura           numeric,
  largura          numeric,
  comprimento      numeric,
  garantia_fabrica integer,
  aplicacao        text,
  descricao_site   text,
  dados_tecnicos   text,
  itens_inclusos   text,
  tem_montagem     text,
  montagem_gratis  text,
  frete_gratis     text,
  aceita_desconto  text,     -- N = produto marcado "sem desconto"
  sem_desconto     text,     -- S = produto ou grupo proíbe desconto
  desconto_grupo   numeric,  -- % máximo do grupo; 0/null = vale o padrão da loja
  busca            text generated always as
                     (fdc.sem_acento(descricao || ' ' || coalesce(marca, '') || ' ' || coalesce(grupo, ''))) stored
);
create index produto_codigo on fdc.produto (codigo);
create index produto_ean on fdc.produto (ean);
create index produto_referencia on fdc.produto (referencia);
create index produto_busca on fdc.produto using gin (busca extensions.gin_trgm_ops);

-- Só saldos > 0.
create table fdc.estoque (
  chave            text primary key,
  hash             text not null,
  sincronizado_em  timestamptz not null default now(),
  codigo           integer not null,
  filial           text not null,
  estoque          numeric not null
);
create index estoque_codigo on fdc.estoque (codigo, filial);

-- Promoções por loja (PRODUTOPROMOCAO) que ainda não acabaram.
create table fdc.promocao (
  chave            text primary key,
  hash             text not null,
  sincronizado_em  timestamptz not null default now(),
  codigo           integer not null,
  filial           text not null,
  inicio           date not null,
  fim              date not null,
  preco            numeric not null
);
create index promocao_codigo on fdc.promocao (codigo, filial);

-- Gravada pela própria Edge Function quando o worker sobe a imagem para o Storage.
create table fdc.foto (
  chave            text primary key,
  hash             text not null,
  sincronizado_em  timestamptz not null default now(),
  codigo           integer not null,
  seq              integer not null,
  url              text not null,
  bytes            integer
);
create index foto_codigo on fdc.foto (codigo, seq);

-- Condições ativas, sem renegociação e sem exclusivas; a validade é conferida na consulta.
create table fdc.condicao (
  chave               text primary key,
  hash                text not null,
  sincronizado_em     timestamptz not null default now(),
  codigo              integer not null,
  descricao           text,
  parcelas            integer not null,
  com_entrada         text,
  fator               numeric,
  desconto_perc       numeric,
  parcela_minima      numeric,
  entrada_minima_perc numeric,
  inicio              date,
  fim                 date
);

create table fdc.condicao_fator (
  chave            text primary key,
  hash             text not null,
  sincronizado_em  timestamptz not null default now(),
  codigo           integer not null,
  filial           text not null,
  fator            numeric
);
create index condicao_fator_codigo on fdc.condicao_fator (codigo, filial);

-- Todas as filiais, inclusive depósito (estoque_por_loja precisa do nome).
create table fdc.loja (
  chave            text primary key,
  hash             text not null,
  sincronizado_em  timestamptz not null default now(),
  filial           text not null,
  nome             text,
  endereco         text,
  numero           text,
  bairro           text,
  cidade           text,
  uf               text,
  cep              text,
  telefones        text,
  email            text,
  ordem            integer
);

create table fdc.cliente (
  chave                text primary key,
  hash                 text not null,
  sincronizado_em      timestamptz not null default now(),
  documento            text not null,
  nome                 text,
  email                text,
  celular              text,
  telefones            text,
  telefone_alternativo text,
  telefones_extra      text,   -- tabela TELEFONES, separados por |
  endereco             text,
  numero               text,
  complemento          text,
  bairro               text,
  cidade               text,
  uf                   text,
  cep                  text,
  cliente_desde        date,
  ultima_compra        date,
  valor_ultima_compra  numeric,
  limite_credito       numeric,
  situacao             text,
  ativo                text,
  doc_digitos          text generated always as (fdc.digitos(documento)) stored,
  -- Todos os telefones só com dígitos, separados por |: "22992033515|2235551234".
  fones                text generated always as (
                         regexp_replace(
                           regexp_replace(
                             coalesce(celular, '') || '|' || coalesce(telefones, '') || '|'
                               || coalesce(telefone_alternativo, '') || '|' || coalesce(telefones_extra, ''),
                             '[ ().-]', '', 'g'),
                           '[^0-9]+', '|', 'g')) stored
);
create index cliente_doc on fdc.cliente (doc_digitos);
create index cliente_fones on fdc.cliente using gin (fones extensions.gin_trgm_ops);

-- Só títulos em aberto (RECEBER.STATUS = '0'). Título pago some daqui.
create table fdc.parcela (
  chave            text primary key,
  hash             text not null,
  sincronizado_em  timestamptz not null default now(),
  filial           text not null,
  titulo           text not null,
  parcela          integer,
  documento        text,
  venda            integer,
  emissao          date,
  vencimento       date,
  valor            numeric,
  forma            text,
  pix_copia_e_cola text,
  codigo_barras    text,
  doc_digitos      text generated always as (fdc.digitos(documento)) stored
);
create index parcela_doc on fdc.parcela (doc_digitos, vencimento);

-- Vendas válidas dos últimos 24 meses.
create table fdc.compra (
  chave             text primary key,
  hash              text not null,
  sincronizado_em   timestamptz not null default now(),
  filial            text not null,
  venda             integer not null,
  documento         text,
  data              date,
  total             numeric,
  nota_fiscal       text,
  entrega_domicilio text,
  data_entrega      date,
  data_montagem     date,
  itens             text,
  itens_pendentes   integer,
  doc_digitos       text generated always as (fdc.digitos(documento)) stored
);
create index compra_doc on fdc.compra (doc_digitos, data);

-- ============================================================ CONTROLE

-- Uma linha por tabela sincronizada: é também a lista do que fdc.aplicar aceita.
create table fdc.sync_estado (
  tabela       text primary key,
  ultima_sync  timestamptz,  -- fim do último ciclo com sucesso
  linhas       integer
);
insert into fdc.sync_estado (tabela) values
  ('produto'), ('estoque'), ('promocao'), ('foto'), ('condicao'), ('condicao_fator'),
  ('loja'), ('cliente'), ('parcela'), ('compra');

create table fdc.worker_status (
  id        smallint primary key default 1 check (id = 1),
  versao    text,
  banco_ok  boolean,
  yaml_sha  text,
  yaml_erro text,
  erros     jsonb not null default '{}',  -- {tabela: mensagem} do último ciclo com erro
  visto_em  timestamptz
);

-- Pedidos da equipe para o worker (diagnóstico, atualizar agora). O worker busca a cada 30 s.
create table fdc.comando (
  id           bigint generated always as identity primary key,
  tipo         text not null check (tipo in ('consulta', 'atualizar')),
  consulta     text,
  params       jsonb not null default '{}',
  criado_em    timestamptz not null default now(),
  pego_em      timestamptz,
  concluido_em timestamptz,
  resultado    jsonb,
  erro         text
);

-- ============================================================ GRAVAÇÃO (worker → Edge Function)

-- Grava (insert ou update pela chave) as linhas e apaga as chaves em p_remover.
-- As colunas vêm do JSON pelo nome; chave desconhecida no JSON é ignorada.
create function fdc.aplicar(p_tabela text, p_linhas jsonb, p_remover text[] default '{}')
returns void language plpgsql as $$
declare
  v_cols text;
  v_sets text;
begin
  if not exists (select 1 from fdc.sync_estado where tabela = p_tabela) then
    raise exception 'tabela % não é sincronizada', p_tabela;
  end if;
  if cardinality(p_remover) > 0 then
    execute format('delete from fdc.%I where chave = any($1)', p_tabela) using p_remover;
  end if;
  if p_linhas is null or jsonb_array_length(p_linhas) = 0 then
    return;
  end if;
  select string_agg(quote_ident(column_name), ', ' order by ordinal_position),
         string_agg(format('%1$I = excluded.%1$I', column_name), ', ' order by ordinal_position)
           filter (where column_name <> 'chave')
    into v_cols, v_sets
    from information_schema.columns
   where table_schema = 'fdc' and table_name = p_tabela
     and is_generated = 'NEVER' and column_name <> 'sincronizado_em';
  execute format(
    'insert into fdc.%1$I (%2$s, sincronizado_em)
     select %2$s, now() from jsonb_populate_recordset(null::fdc.%1$I, $1)
     on conflict (chave) do update set %3$s, sincronizado_em = excluded.sincronizado_em',
    p_tabela, v_cols, v_sets) using p_linhas;
end $$;

-- ============================================================ CONSULTAS DA IA (Edge Function /q/{nome})
--
-- Cada fdc.q_* devolve uma linha JSON por resultado, já na ordem certa. Os parâmetros chegam
-- normalizados pela Edge Function (documento só com dígitos, termos sem acento etc.).

-- Preço promocional vigente hoje: promoção da loja ou, se não houver, a do cadastro do produto.
create function fdc.preco_promocional(p_codigo integer, p_filial text) returns numeric
language sql stable as $$
  select coalesce(
    (select pp.preco from fdc.promocao pp
      where pp.codigo = p_codigo and pp.filial = p_filial and fdc.hoje() between pp.inicio and pp.fim
      order by pp.inicio desc limit 1),
    (select p.promocao_preco from fdc.produto p
      where p.codigo = p_codigo and p.promocao_preco > 0
        and fdc.hoje() between coalesce(p.promocao_inicio, fdc.hoje()) and coalesce(p.promocao_fim, fdc.hoje())
      limit 1))
$$;

create function fdc.promocao_ate(p_codigo integer, p_filial text) returns date
language sql stable as $$
  select coalesce(
    (select pp.fim from fdc.promocao pp
      where pp.codigo = p_codigo and pp.filial = p_filial and fdc.hoje() between pp.inicio and pp.fim
      order by pp.inicio desc limit 1),
    (select p.promocao_fim from fdc.produto p
      where p.codigo = p_codigo and p.promocao_preco > 0
        and fdc.hoje() between coalesce(p.promocao_inicio, fdc.hoje()) and coalesce(p.promocao_fim, fdc.hoje())
      limit 1))
$$;

-- p_busca: texto inteiro (código, EAN, referência). p_termos: termos separados por espaço.
create function fdc.q_buscar_produto(p_busca text, p_termos text, p_filial text) returns setof json
language sql stable as $$
  with t as (
    select coalesce(array_agg('%' || x || '%'), '{}') as padroes, (array_agg(x))[1] as primeiro
      from unnest(string_to_array(nullif(p_termos, ''), ' ')) x
  ), achados as (
    select p.codigo, p.descricao, p.marca, p.grupo, p.unidade, p.preco_avista, p.preco_aprazo,
           coalesce(e.estoque, 0) as estoque_loja,
           coalesce((select sum(e2.estoque) from fdc.estoque e2 where e2.codigo = p.codigo), 0) as estoque_total,
           (p.busca like '%' || t.primeiro || '%') as no_nome
      from fdc.produto p
      cross join t
      left join fdc.estoque e on e.codigo = p.codigo and e.filial = p_filial
     where coalesce(p.fora_de_linha, 'N') = 'N' and p.preco_avista > 0
       and ((cardinality(t.padroes) > 0 and p.busca like all (t.padroes))
            or p.codigo::text = p_busca or p.ean = p_busca or p.referencia = p_busca)
  )
  select row_to_json(x) from (
    select a.codigo, a.descricao, a.marca, a.grupo, a.unidade, a.preco_avista, a.preco_aprazo,
           fdc.preco_promocional(a.codigo, p_filial) as preco_promocional,
           a.estoque_loja, a.estoque_total,
           (select f.url from fdc.foto f where f.codigo = a.codigo order by f.seq limit 1) as foto_url
      from achados a
     order by (a.estoque_loja > 0) desc, a.estoque_total desc, a.no_nome desc nulls last, a.descricao
  ) x
$$;

create function fdc.q_detalhe_produto(p_codigo integer, p_filial text) returns setof json
language sql stable as $$
  select row_to_json(x) from (
    select p.codigo, p.descricao,
           case when p.fora_de_linha = 'S' then 'N' else 'S' end as ativo,
           p.marca, p.grupo, p.subgrupo, p.referencia, p.ean, p.unidade,
           p.preco_avista, p.preco_aprazo,
           fdc.preco_promocional(p.codigo, p_filial) as preco_promocional,
           fdc.promocao_ate(p.codigo, p_filial) as promocao_ate,
           coalesce(e.estoque, 0) as estoque_loja,
           coalesce((select sum(e2.estoque) from fdc.estoque e2 where e2.codigo = p.codigo), 0) as estoque_total,
           p.altura, p.largura, p.comprimento, p.garantia_fabrica, p.aplicacao,
           p.descricao_site, p.dados_tecnicos, p.itens_inclusos,
           p.tem_montagem, p.montagem_gratis, p.frete_gratis, p.aceita_desconto,
           (select f.url from fdc.foto f where f.codigo = p.codigo order by f.seq limit 1) as foto_url,
           (select count(*)::int from fdc.foto f where f.codigo = p.codigo) as fotos
      from fdc.produto p
      left join fdc.estoque e on e.codigo = p.codigo and e.filial = p_filial
     where p.codigo = p_codigo
  ) x
$$;

create function fdc.q_fotos_produto(p_codigo integer) returns setof json
language sql stable as $$
  select row_to_json(x) from (
    select f.seq, f.url from fdc.foto f where f.codigo = p_codigo order by f.seq
  ) x
$$;

create function fdc.q_estoque_por_loja(p_codigo integer) returns setof json
language sql stable as $$
  select row_to_json(x) from (
    select e.filial, l.nome as loja, l.cidade, e.estoque
      from fdc.estoque e
      left join fdc.loja l on l.filial = e.filial
     where e.codigo = p_codigo and e.estoque > 0
     order by e.estoque desc
  ) x
$$;

create function fdc.q_promocoes_vigentes(p_filial text, p_termos text) returns setof json
language sql stable as $$
  with t as (
    select coalesce(array_agg('%' || x || '%'), '{}') as padroes
      from unnest(string_to_array(nullif(p_termos, ''), ' ')) x
  ), promo as (
    select pp.codigo, pp.preco as preco_promocional, pp.fim as valida_ate
      from fdc.promocao pp
     where pp.filial = p_filial and fdc.hoje() between pp.inicio and pp.fim
    union all
    select p.codigo, p.promocao_preco, p.promocao_fim
      from fdc.produto p
     where p.promocao_preco > 0
       and fdc.hoje() between coalesce(p.promocao_inicio, fdc.hoje()) and coalesce(p.promocao_fim, fdc.hoje())
       and not exists (select 1 from fdc.promocao pp2
                        where pp2.codigo = p.codigo and pp2.filial = p_filial
                          and fdc.hoje() between pp2.inicio and pp2.fim)
  )
  select row_to_json(x) from (
    select p.codigo, p.descricao, p.marca,
           p.preco_avista as preco_normal, pr.preco_promocional, pr.valida_ate,
           coalesce(e.estoque, 0) as estoque_loja,
           round((p.preco_avista - pr.preco_promocional) * 100 / nullif(p.preco_avista, 0), 1) as desconto_perc
      from promo pr
      join fdc.produto p on p.codigo = pr.codigo
      cross join t
      left join fdc.estoque e on e.codigo = pr.codigo and e.filial = p_filial
     where p.busca like all (t.padroes)
     order by desconto_perc desc nulls last
  ) x
$$;

create function fdc.q_condicoes_pagamento(p_filial text) returns setof json
language sql stable as $$
  select row_to_json(x) from (
    select c.codigo, c.descricao, c.parcelas, c.com_entrada,
           coalesce(nullif(cf.fator, 0), nullif(c.fator, 0), 1) as fator,
           c.desconto_perc, c.parcela_minima, c.entrada_minima_perc, c.fim as valida_ate
      from fdc.condicao c
      left join fdc.condicao_fator cf on cf.codigo = c.codigo and cf.filial = p_filial
     where (c.inicio is null or c.inicio <= fdc.hoje()) and (c.fim is null or c.fim >= fdc.hoje())
     order by c.parcelas, c.descricao
  ) x
$$;

create function fdc.q_simular_parcelamento(p_valor numeric, p_filial text) returns setof json
language sql stable as $$
  select row_to_json(x) from (
    select y.codigo, y.descricao, y.parcelas, y.com_entrada, y.fator,
           round(y.total, 2) as total, round(y.total / y.parcelas, 2) as valor_parcela
      from (
        select c.codigo, c.descricao, c.parcelas, c.com_entrada, c.parcela_minima,
               coalesce(nullif(cf.fator, 0), nullif(c.fator, 0), 1) as fator,
               p_valor * coalesce(nullif(cf.fator, 0), nullif(c.fator, 0), 1) as total
          from fdc.condicao c
          left join fdc.condicao_fator cf on cf.codigo = c.codigo and cf.filial = p_filial
         where (c.inicio is null or c.inicio <= fdc.hoje()) and (c.fim is null or c.fim >= fdc.hoje())
           and c.parcelas > 0
      ) y
     where y.parcela_minima is null or y.parcela_minima <= 0 or y.total / y.parcelas >= y.parcela_minima
     order by y.parcelas, y.total
  ) x
$$;

-- p_desconto_maximo_loja vem fixo da Edge Function (a IA não consegue mudar).
create function fdc.q_negociar_preco(p_codigo integer, p_valor_pedido numeric, p_filial text,
                                     p_desconto_maximo_loja numeric) returns setof json
language sql stable as $$
  select row_to_json(z) from (
    select y.codigo, y.descricao, y.preco_referencia, y.valor_pedido,
           round(y.preco_referencia * (1 - y.desconto_maximo / 100), 2) as preco_minimo,
           round((1 - y.valor_pedido / nullif(y.preco_referencia, 0)) * 100, 2) as desconto_pedido_perc,
           case when y.valor_pedido >= round(y.preco_referencia * (1 - y.desconto_maximo / 100), 2)
                then 'S' else 'N' end as pode_aceitar
      from (
        select p.codigo, p.descricao,
               coalesce(pr.promo, p.preco_avista) as preco_referencia,
               case when pr.promo is not null or p.sem_desconto = 'S' then 0
                    else coalesce(nullif(p.desconto_grupo, 0), p_desconto_maximo_loja) end as desconto_maximo,
               p_valor_pedido as valor_pedido
          from fdc.produto p
          cross join lateral (select fdc.preco_promocional(p.codigo, p_filial) as promo) pr
         where p.codigo = p_codigo
      ) y
  ) z
$$;

-- p_final8: últimos 8 dígitos do telefone do WhatsApp.
create function fdc.q_cliente_por_telefone(p_final8 text) returns setof json
language sql stable as $$
  select row_to_json(x) from (
    select c.nome, right(c.doc_digitos, 4) as documento_final4, c.cidade, c.bairro,
           c.cliente_desde, c.ultima_compra, c.ativo
      from fdc.cliente c
     where c.fones like '%' || p_final8 || '%'
     order by c.ultima_compra desc nulls last
  ) x
$$;

-- p_documento: CPF/CNPJ só com dígitos.
create function fdc.q_cliente_por_documento(p_documento text) returns setof json
language sql stable as $$
  select row_to_json(x) from (
    select c.nome, c.documento, c.email, c.celular, c.telefones,
           c.endereco, c.numero, c.complemento, c.bairro, c.cidade, c.uf, c.cep,
           c.cliente_desde, c.ultima_compra, c.valor_ultima_compra, c.limite_credito, c.situacao, c.ativo,
           coalesce((select sum(r.valor) from fdc.parcela r where r.doc_digitos = c.doc_digitos), 0) as saldo_devedor,
           (select count(*)::int from fdc.parcela r
             where r.doc_digitos = c.doc_digitos and r.vencimento < fdc.hoje()) as parcelas_atrasadas
      from fdc.cliente c
     where c.doc_digitos = p_documento
  ) x
$$;

create function fdc.q_parcelas_em_aberto(p_documento text) returns setof json
language sql stable as $$
  select row_to_json(x) from (
    select r.filial, r.titulo, r.parcela, r.venda, r.emissao, r.vencimento, r.valor,
           greatest(fdc.hoje() - r.vencimento, 0) as dias_atraso,
           r.forma, r.pix_copia_e_cola, r.codigo_barras
      from fdc.parcela r
     where r.doc_digitos = p_documento
     order by r.vencimento
  ) x
$$;

create function fdc.q_compras_cliente(p_documento text) returns setof json
language sql stable as $$
  select row_to_json(x) from (
    select c.filial, c.venda, c.data, c.total, c.nota_fiscal, c.entrega_domicilio,
           c.data_entrega, c.data_montagem, c.itens, c.itens_pendentes
      from fdc.compra c
     where c.doc_digitos = p_documento
     order by c.data desc
  ) x
$$;

create function fdc.q_lojas() returns setof json
language sql stable as $$
  select row_to_json(x) from (
    select l.filial, l.nome, l.endereco, l.numero, l.bairro, l.cidade, l.uf, l.cep, l.telefones, l.email
      from fdc.loja l
     where coalesce(trim(l.endereco), '') <> ''  -- depósito/encomenda não têm endereço
     order by coalesce(l.ordem, 999), l.filial
  ) x
$$;

-- ============================================================ ACESSO

-- Ninguém além do dono (a Edge Function conecta como postgres) lê ou executa nada em fdc.
revoke all on all tables in schema fdc from public, anon, authenticated;
revoke all on all functions in schema fdc from public, anon, authenticated;
alter default privileges in schema fdc revoke all on tables from public, anon, authenticated;
alter default privileges in schema fdc revoke execute on functions from public, anon, authenticated;
do $$
declare t text;
begin
  for t in select tablename from pg_tables where schemaname = 'fdc' loop
    execute format('alter table fdc.%I enable row level security', t);
  end loop;
end $$;

-- Fotos dos produtos: bucket público (o WhatsApp baixa pela URL). Sem policy de listagem.
insert into storage.buckets (id, name, public, file_size_limit, allowed_mime_types)
values ('fotos', 'fotos', true, 5242880, array['image/jpeg', 'image/png', 'image/webp', 'image/gif'])
on conflict (id) do nothing;
