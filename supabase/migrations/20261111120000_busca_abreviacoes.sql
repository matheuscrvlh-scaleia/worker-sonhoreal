-- O ERP grava a descrição abreviada ("FOGAO 4 BC SUGGAR ... PTO") e o cliente escreve por extenso
-- ("fogão 4 bocas preto"): a busca exige todos os termos e voltava vazia, e a IA transferia
-- para humano. A coluna busca passa a ter também as palavras por extenso das abreviações.
-- Lista tirada das descrições reais em 11/11/2026; só entram abreviações sem ambiguidade.
-- Forma feminina junto quando o cliente costuma usá-la ("preta", "branca").

create function fdc.expandir_abreviacoes(t text) returns text
language sql immutable parallel safe as $$
  select coalesce(string_agg(distinct e.completo, ' '), '')
    from unnest(regexp_split_to_array(upper(coalesce(t, '')), '[^A-Z0-9]+')) w
    join (values
      ('PTO', 'PRETO PRETA'), ('BCO', 'BRANCO BRANCA'), ('PTA', 'PRATA'),
      ('VERM', 'VERMELHO VERMELHA'), ('GRAF', 'GRAFITE'), ('CARV', 'CARVALHO'),
      ('NOG', 'NOGUEIRA'), ('ARD', 'ARDOSIA'),
      ('BC', 'BOCAS'), ('PT', 'PORTAS'), ('PTS', 'PORTAS'), ('GAV', 'GAVETAS'),
      ('LUG', 'LUGARES'), ('ESP', 'ESPELHO'), ('CAD', 'CADEIRAS'), ('VD', 'VIDRO'),
      ('TEC', 'TECIDO'), ('VEL', 'VELUDO'), ('RET', 'RETRATIL'), ('RECL', 'RECLINAVEL'),
      ('TORN', 'TORNEADO'), ('MAD', 'MADEIRA'), ('RIP', 'RIPADO'), ('DEC', 'DECORATIVA'),
      ('CONJ', 'CONJUNTO'), ('ESTOF', 'ESTOFADO'),
      ('BIV', 'BIVOLT'), ('LT', 'LITROS'), ('XIC', 'XICARAS'), ('PCS', 'PECAS')
    ) e(abrev, completo) on e.abrev = w
$$;

drop index fdc.produto_busca;
alter table fdc.produto drop column busca;
alter table fdc.produto add column busca text generated always as
  (fdc.sem_acento(descricao || ' ' || coalesce(marca, '') || ' ' || coalesce(grupo, '')
                  || ' ' || fdc.expandir_abreviacoes(descricao))) stored;
create index produto_busca on fdc.produto using gin (busca extensions.gin_trgm_ops);
