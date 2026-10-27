// Edge Function "fdc": porta de entrada da cópia do ERP no Supabase.
//
//   GET  /status                       sem token; 200 se o worker deu sinal nos últimos 10 min, 503 se não
//   GET  /q                            (ia, admin) lista as consultas, para montar as tools da IA
//   GET  /q/{nome}?a=1  | POST JSON    (ia, admin) executa uma consulta (consultas.ts)
//
//   GET  /sync/ping                    (worker) confere token e conexão
//   POST /sync/status                  (worker) sinal de vida: versão, banco ok, erros
//   GET  /sync/{tabela}/hashes         (worker) chave e hash de tudo que está gravado (paginado)
//   POST /sync/{tabela}                (worker) {linhas: [...], remover: [chaves]}
//   POST /sync/{tabela}/concluir       (worker) fim de um ciclo com sucesso: {linhas: total}
//   PUT  /sync/foto/{codigo}/{seq}     (worker) imagem no body, ?hash=; grava no Storage
//   POST /sync/foto/remover            (worker) {chaves: [...]}
//   GET  /sync/comandos                (worker) pega os pedidos pendentes da equipe
//   POST /sync/comandos/{id}           (worker) {resultado} ou {erro}
//
//   POST /admin/comandos               (admin) {tipo: "consulta", consulta: "diag_versao", params: {}}
//                                      ou {tipo: "atualizar"}; o worker executa em até 30 s
//   GET  /admin/comandos[/{id}]        (admin) resultado
//
// Tokens nos secrets da função: FDC_TOKEN_IA, FDC_TOKEN_ADMIN, FDC_TOKEN_WORKER. Vão no header
// Authorization: Bearer <token> (ou X-Token). Publicar com verify_jwt = false (config.toml).

import postgres from "npm:postgres@3.4.5";
import { CONSULTAS } from "./consultas.ts";
import { converter, ErroParametro } from "./params.ts";

const SUPABASE_URL = Deno.env.get("SUPABASE_URL")!;
const SERVICE_KEY = Deno.env.get("SUPABASE_SERVICE_ROLE_KEY")!;
const BUCKET = "fotos";
const TABELAS = new Set([
  "produto", "estoque", "promocao", "foto", "condicao", "condicao_fator", "loja", "cliente", "parcela", "compra",
]);
const PAGINA_HASHES = 5000;

const sql = postgres(Deno.env.get("SUPABASE_DB_URL")!, { prepare: false, max: 4, idle_timeout: 30 });

type Papel = "ia" | "admin" | "worker";
const tokens = (["ia", "admin", "worker"] as Papel[]).map((p) => [p, Deno.env.get(`FDC_TOKEN_${p.toUpperCase()}`) ?? ""] as const);

Deno.serve(async (req) => {
  const url = new URL(req.url);
  const partes = url.pathname.split("/").filter(Boolean);
  if (partes[0] === "fdc") partes.shift();
  try {
    return await rotear(req, url, partes);
  } catch (e) {
    if (e instanceof ErroParametro) return erro(400, e.message);
    console.error(e);
    // deno-lint-ignore no-explicit-any
    const codigo = (e as any)?.code;
    if (["CONNECT_TIMEOUT", "CONNECTION_CLOSED", "CONNECTION_ENDED", "ECONNREFUSED", "ECONNRESET"].includes(codigo)) {
      return erro(503, "banco do Supabase indisponível");
    }
    return erro(500, e instanceof Error ? e.message : String(e));
  }
});

async function rotear(req: Request, url: URL, p: string[]): Promise<Response> {
  const m = req.method;
  const papel = papelDe(req);
  if (m === "GET" && p.length === 1 && p[0] === "status") return status(papel);
  if (!papel) return erro(401, "token inválido ou ausente");

  switch (p[0]) {
    case "q":
      if (papel === "worker") return erro(403, "o token do worker não faz consultas");
      if (p.length === 1 && m === "GET") return listar();
      if (p.length === 2 && (m === "GET" || m === "POST")) return executar(req, url, p[1]);
      break;
    case "sync":
      if (papel !== "worker") return erro(403, "rota só do worker");
      return sync(req, url, p.slice(1));
    case "admin":
      if (papel !== "admin") return erro(403, "rota exige token_admin");
      return admin(req, p.slice(1));
  }
  return erro(404, "rota não existe");
}

// ---------- IA

function listar(): Response {
  const consultas = [...CONSULTAS.values()].map((c) => ({
    nome: c.nome,
    descricao: c.descricao,
    params: c.params.filter((x) => !x.fixo).map((x) => ({
      nome: x.nome,
      tipo: x.tipo,
      descricao: x.descricao,
      obrigatorio: !!x.obrigatorio,
      padrao: x.padrao,
      exemplo: x.exemplo,
    })),
  }));
  return json(200, { consultas });
}

async function executar(req: Request, url: URL, nome: string): Promise<Response> {
  const c = CONSULTAS.get(nome);
  if (!c) return erro(404, `consulta não existe: ${nome}`);
  const recebidos = await lerParams(req, url);
  const valores: Record<string, string | number | null> = {};
  for (const prm of c.params) Object.assign(valores, converter(prm, recebidos[prm.nome]));

  const max = c.max_linhas ?? 200;
  // funcao e tipos vêm do catálogo (constantes), nunca de quem chamou.
  const marcadores = c.args.map(([, tipo], i) => `$${i + 1}::${tipo}`).join(", ");
  const ini = performance.now();
  const linhas = await sql.unsafe(
    `select r from fdc.${c.funcao}(${marcadores}) r limit ${max + 1}`,
    c.args.map(([n]) => valores[n] ?? null),
  );
  const ms = Math.round(performance.now() - ini);
  const [{ atualizado_em }] = await sql`
    select min(ultima_sync) as atualizado_em from fdc.sync_estado where tabela in ${sql(c.tabelas)}`;

  const saida = linhas.slice(0, max).map((l) => l.r);
  return json(200, { consulta: nome, total: saida.length, truncado: linhas.length > max, ms, atualizado_em, linhas: saida });
}

async function lerParams(req: Request, url: URL): Promise<Record<string, string>> {
  const out: Record<string, string> = Object.fromEntries(url.searchParams);
  if (req.method === "POST") {
    for (const [k, v] of Object.entries(await corpo(req))) {
      if (v !== null && v !== undefined) out[k] = String(v);
    }
  }
  return out;
}

async function status(papel: Papel | null): Promise<Response> {
  const [w] = await sql`
    select versao, banco_ok, visto_em, erros, extract(epoch from now() - visto_em)::int as segundos
      from fdc.worker_status where id = 1`;
  const atrasado = !w || w.segundos > 600;
  // deno-lint-ignore no-explicit-any
  const out: Record<string, any> = {
    ok: !atrasado,
    worker_visto_em: w?.visto_em ?? null,
    worker_versao: w?.versao ?? null,
    banco_erp_ok: w?.banco_ok ?? null,
  };
  if (papel === "ia" || papel === "admin") {
    out.erros = w?.erros ?? {};
    out.tabelas = await sql`select tabela, ultima_sync, linhas from fdc.sync_estado order by tabela`;
  }
  return json(atrasado ? 503 : 200, out);
}

// ---------- worker

async function sync(req: Request, url: URL, p: string[]): Promise<Response> {
  const m = req.method;
  if (m === "GET" && p[0] === "ping") return json(200, { ok: true });

  if (m === "POST" && p.length === 1 && p[0] === "status") {
    const b = await corpo(req);
    await sql`
      insert into fdc.worker_status (id, versao, banco_ok, yaml_sha, yaml_erro, erros, visto_em)
      values (1, ${b.versao ?? null}, ${b.banco_ok ?? null}, ${b.yaml_sha ?? null}, ${b.yaml_erro ?? null},
              ${sql.json(b.erros ?? {})}, now())
      on conflict (id) do update set versao = excluded.versao, banco_ok = excluded.banco_ok,
        yaml_sha = excluded.yaml_sha, yaml_erro = excluded.yaml_erro, erros = excluded.erros, visto_em = now()`;
    return json(200, { ok: true });
  }

  if (p[0] === "comandos") {
    if (m === "GET" && p.length === 1) {
      const comandos = await sql`
        update fdc.comando set pego_em = now()
         where id in (select id from fdc.comando where pego_em is null order by id limit 5)
        returning id, tipo, consulta, params`;
      return json(200, { comandos });
    }
    if (m === "POST" && p.length === 2) {
      const b = await corpo(req);
      await sql`
        update fdc.comando set concluido_em = now(), erro = ${b.erro ?? null},
               resultado = ${b.resultado === undefined || b.resultado === null ? null : sql.json(b.resultado)}
         where id = ${p[1]}`;
      return json(200, { ok: true });
    }
  }

  if (p[0] === "foto") {
    if (m === "PUT" && p.length === 3) return enviarFoto(req, url, p[1], p[2]);
    if (m === "POST" && p.length === 2 && p[1] === "remover") return removerFotos(await corpo(req));
  }

  const tabela = p[0];
  if (!TABELAS.has(tabela)) return erro(404, `tabela não sincronizada: ${tabela}`);

  if (m === "GET" && p.length === 2 && p[1] === "hashes") {
    const depois = url.searchParams.get("depois") ?? "";
    // tabela já validada contra TABELAS.
    const linhas = await sql.unsafe(
      `select chave, hash from fdc.${tabela} where chave > $1 order by chave limit ${PAGINA_HASHES}`,
      [depois],
    );
    return json(200, { hashes: linhas.map((l) => [l.chave, l.hash]), fim: linhas.length < PAGINA_HASHES });
  }
  if (m === "POST" && p.length === 1) {
    if (tabela === "foto") return erro(400, "fotos vão por PUT /sync/foto/{codigo}/{seq}");
    const b = await corpo(req);
    await sql`
      select fdc.aplicar(${tabela}, ${sql.json(b.linhas ?? [])},
                         array(select jsonb_array_elements_text(${sql.json(b.remover ?? [])})))`;
    return json(200, { ok: true });
  }
  if (m === "POST" && p.length === 2 && p[1] === "concluir") {
    const b = await corpo(req);
    await sql`update fdc.sync_estado set ultima_sync = now(), linhas = ${b.linhas ?? null} where tabela = ${tabela}`;
    return json(200, { ok: true });
  }
  return erro(404, "rota não existe");
}

const extensoes: Record<string, string> = { "image/jpeg": "jpg", "image/png": "png", "image/webp": "webp", "image/gif": "gif" };

async function enviarFoto(req: Request, url: URL, codigoTxt: string, seqTxt: string): Promise<Response> {
  const codigo = Number(codigoTxt), seq = Number(seqTxt);
  const hash = url.searchParams.get("hash") ?? "";
  if (!Number.isInteger(codigo) || !Number.isInteger(seq) || !/^[0-9a-f]{8,64}$/.test(hash)) {
    return erro(400, "codigo, seq ou hash inválido");
  }
  const tipo = (req.headers.get("content-type") ?? "").split(";")[0].trim();
  const ext = extensoes[tipo];
  if (!ext) return erro(415, `tipo de imagem não aceito: ${tipo}`);
  const bytes = new Uint8Array(await req.arrayBuffer());
  if (bytes.length === 0 || bytes.length > 5 << 20) return erro(413, "imagem vazia ou maior que 5 MB");

  // O hash no nome faz a URL mudar quando a foto muda: nenhum cache serve a foto velha.
  const caminho = `${codigo}/${seq}-${hash.slice(0, 12)}.${ext}`;
  const r = await fetch(`${SUPABASE_URL}/storage/v1/object/${BUCKET}/${caminho}`, {
    method: "POST",
    headers: { ...autStorage(), "content-type": tipo, "x-upsert": "true", "cache-control": "max-age=31536000" },
    body: bytes,
  });
  if (!r.ok) return erro(502, `Storage recusou a foto: ${r.status} ${await r.text()}`);

  const chave = `${codigo}|${seq}`;
  const publica = `${SUPABASE_URL}/storage/v1/object/public/${BUCKET}/${caminho}`;
  const [antiga] = await sql`select url from fdc.foto where chave = ${chave}`;
  await sql`
    insert into fdc.foto (chave, hash, codigo, seq, url, bytes)
    values (${chave}, ${hash}, ${codigo}, ${seq}, ${publica}, ${bytes.length})
    on conflict (chave) do update set hash = excluded.hash, url = excluded.url, bytes = excluded.bytes,
      sincronizado_em = now()`;
  if (antiga && antiga.url !== publica) await removerObjetos([antiga.url]);
  return json(200, { url: publica });
}

// deno-lint-ignore no-explicit-any
async function removerFotos(b: Record<string, any>): Promise<Response> {
  const chaves: string[] = Array.isArray(b.chaves) ? b.chaves.map(String) : [];
  if (chaves.length === 0) return json(200, { removidas: 0 });
  const linhas = await sql`delete from fdc.foto where chave in ${sql(chaves)} returning url`;
  await removerObjetos(linhas.map((l) => l.url));
  return json(200, { removidas: linhas.length });
}

// Objeto que sobrar no Storage não atrapalha a IA: só registra o erro.
async function removerObjetos(urls: string[]) {
  const prefixes = urls.map((u) => u.split(`/object/public/${BUCKET}/`)[1]).filter(Boolean);
  if (prefixes.length === 0) return;
  const r = await fetch(`${SUPABASE_URL}/storage/v1/object/${BUCKET}`, {
    method: "DELETE",
    headers: { ...autStorage(), "content-type": "application/json" },
    body: JSON.stringify({ prefixes }),
  });
  if (!r.ok) console.error(`removendo fotos do Storage: ${r.status} ${await r.text()}`);
}

const autStorage = () => ({ authorization: `Bearer ${SERVICE_KEY}`, apikey: SERVICE_KEY });

// ---------- equipe

async function admin(req: Request, p: string[]): Promise<Response> {
  if (p[0] !== "comandos") return erro(404, "rota não existe");
  if (req.method === "POST" && p.length === 1) {
    const b = await corpo(req);
    if (b.tipo !== "consulta" && b.tipo !== "atualizar") throw new ErroParametro(`tipo deve ser "consulta" ou "atualizar"`);
    if (b.tipo === "consulta" && !b.consulta) throw new ErroParametro("informe consulta (um diag_* do queries.yaml)");
    const [c] = await sql`
      insert into fdc.comando (tipo, consulta, params)
      values (${b.tipo}, ${b.consulta ?? null}, ${sql.json(b.params ?? {})})
      returning id, criado_em`;
    return json(202, { ...c, aviso: "o worker executa em até 30 s; veja em GET /admin/comandos/{id}" });
  }
  if (req.method === "GET" && p.length === 2) {
    const [c] = await sql`select * from fdc.comando where id = ${p[1]}`;
    return c ? json(200, c) : erro(404, "comando não existe");
  }
  if (req.method === "GET" && p.length === 1) {
    return json(200, {
      comandos: await sql`
        select id, tipo, consulta, criado_em, pego_em, concluido_em, erro from fdc.comando order by id desc limit 20`,
    });
  }
  return erro(404, "rota não existe");
}

// ---------- apoio

function papelDe(req: Request): Papel | null {
  const h = req.headers.get("authorization") ?? "";
  const t = (h.toLowerCase().startsWith("bearer ") ? h.slice(7) : req.headers.get("x-token") ?? "").trim();
  if (!t) return null;
  for (const [p, esperado] of tokens) {
    if (esperado.length >= 16 && iguais(t, esperado)) return p;
  }
  return null;
}

// Comparação em tempo constante, para o tempo de resposta não vazar o token.
function iguais(a: string, b: string): boolean {
  const x = new TextEncoder().encode(a), y = new TextEncoder().encode(b);
  if (x.length !== y.length) return false;
  let d = 0;
  for (let i = 0; i < x.length; i++) d |= x[i] ^ y[i];
  return d === 0;
}

// deno-lint-ignore no-explicit-any
async function corpo(req: Request): Promise<Record<string, any>> {
  const texto = await req.text();
  if (!texto.trim()) return {};
  let v: unknown;
  try {
    v = JSON.parse(texto);
  } catch {
    throw new ErroParametro("body não é JSON válido");
  }
  if (typeof v !== "object" || v === null || Array.isArray(v)) throw new ErroParametro("body deve ser um objeto JSON");
  // deno-lint-ignore no-explicit-any
  return v as Record<string, any>;
}

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json; charset=utf-8" } });

const erro = (status: number, msg: string) => json(status, { erro: msg });
