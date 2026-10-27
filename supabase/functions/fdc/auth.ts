// Usuários da IA e da equipe, no Supabase Auth: senha guardada com hash, limite de tentativas
// e tokens JWT de 1 h. O papel ("ia" ou "admin") fica em app_metadata.papel, que só o servidor
// (service role) consegue gravar. Usuário sem papel não entra, mesmo com a senha certa.

import { createRemoteJWKSet, jwtVerify } from "npm:jose@5.9.6";
import { ErroParametro } from "./params.ts";

const SUPABASE_URL = Deno.env.get("SUPABASE_URL")!;
const ANON_KEY = Deno.env.get("SUPABASE_ANON_KEY")!;
const SERVICE_KEY = Deno.env.get("SUPABASE_SERVICE_ROLE_KEY")!;
const AUTH = `${SUPABASE_URL}/auth/v1`;
// Chaves públicas (ES256) do projeto: o token é validado aqui, sem chamar o Auth a cada consulta.
const jwks = createRemoteJWKSet(new URL(`${AUTH}/.well-known/jwks.json`));

export type PapelUsuario = "ia" | "admin";
const PAPEIS: string[] = ["ia", "admin"];

// O Supabase Auth exige e-mail: o usuário "ia-sonhoreal" vira "ia-sonhoreal@usuarios.fdc".
const DOMINIO = "usuarios.fdc";
const emailDe = (usuario: string) => {
  const u = usuario.trim().toLowerCase();
  return u.includes("@") ? u : `${u}@${DOMINIO}`;
};
const usuarioDe = (email: string) => (email.endsWith(`@${DOMINIO}`) ? email.slice(0, -DOMINIO.length - 1) : email);

// Erro com status HTTP próprio (401, 403, 404, 429).
export class ErroAuth extends Error {
  constructor(public status: number, msg: string) {
    super(msg);
  }
}

// deno-lint-ignore no-explicit-any
type Corpo = Record<string, any>;

// POST /auth/token: {usuario, senha} ou {grant_type: "refresh_token", refresh_token}.
export async function emitirToken(b: Corpo): Promise<Corpo> {
  const renovar = b.grant_type === "refresh_token";
  let corpo: Corpo;
  if (renovar) {
    if (!b.refresh_token) throw new ErroParametro("informe refresh_token");
    corpo = { refresh_token: String(b.refresh_token) };
  } else {
    if (!b.usuario || !b.senha) throw new ErroParametro("informe usuario e senha");
    corpo = { email: emailDe(String(b.usuario)), password: String(b.senha) };
  }
  const r = await fetch(`${AUTH}/token?grant_type=${renovar ? "refresh_token" : "password"}`, {
    method: "POST",
    headers: { apikey: ANON_KEY, "content-type": "application/json" },
    body: JSON.stringify(corpo),
  });
  const j = await r.json().catch(() => ({}));
  if (r.status === 429) throw new ErroAuth(429, "muitas tentativas; espere alguns minutos");
  if (!r.ok) throw new ErroAuth(401, renovar ? "refresh_token inválido ou expirado" : "usuário ou senha inválidos");
  const papel = j.user?.app_metadata?.papel;
  if (!PAPEIS.includes(papel)) throw new ErroAuth(403, "usuário sem acesso à API");
  return {
    access_token: j.access_token,
    token_type: "bearer",
    expires_in: j.expires_in,
    expires_at: j.expires_at,
    refresh_token: j.refresh_token,
    usuario: usuarioDe(j.user.email),
    papel,
  };
}

// Valida o Bearer: assinatura, emissor, validade e papel.
export async function verificar(token: string): Promise<{ papel: PapelUsuario; usuario: string } | null> {
  if (token.split(".").length !== 3) return null;
  try {
    const { payload } = await jwtVerify(token, jwks, { issuer: AUTH, audience: "authenticated" });
    // deno-lint-ignore no-explicit-any
    const papel = (payload.app_metadata as any)?.papel;
    if (!PAPEIS.includes(papel)) return null;
    return { papel, usuario: usuarioDe(String(payload.email ?? "")) };
  } catch {
    return null;
  }
}

// ---------- gestão de usuários (rotas /admin/usuarios)

async function adminAuth(metodo: string, caminho: string, corpo?: Corpo): Promise<Corpo> {
  const r = await fetch(`${AUTH}/admin${caminho}`, {
    method: metodo,
    headers: { apikey: SERVICE_KEY, authorization: `Bearer ${SERVICE_KEY}`, "content-type": "application/json" },
    body: corpo ? JSON.stringify(corpo) : undefined,
  });
  const j = await r.json().catch(() => ({}));
  if (!r.ok) {
    const msg = j.msg ?? j.message ?? j.error_description ?? `Supabase Auth: HTTP ${r.status}`;
    throw new ErroAuth(r.status >= 500 ? 502 : 400, msg);
  }
  return j;
}

// deno-lint-ignore no-explicit-any
async function todos(): Promise<any[]> {
  return (await adminAuth("GET", "/users?per_page=1000")).users ?? [];
}

export async function listarUsuarios(): Promise<Corpo[]> {
  return (await todos())
    .filter((u) => PAPEIS.includes(u.app_metadata?.papel))
    .map((u) => ({
      usuario: usuarioDe(u.email),
      papel: u.app_metadata.papel,
      criado_em: u.created_at,
      ultimo_login: u.last_sign_in_at ?? null,
    }));
}

// Cria o usuário ou, se já existir, troca a senha e/ou o papel.
export async function salvarUsuario(b: Corpo): Promise<Corpo> {
  const usuario = String(b.usuario ?? "").trim().toLowerCase();
  if (!/^[a-z0-9._-]{3,40}$/.test(usuario)) {
    throw new ErroParametro("usuario: 3 a 40 caracteres, só letras minúsculas, números, ponto, hífen e _");
  }
  if (b.papel !== undefined && !PAPEIS.includes(b.papel)) throw new ErroParametro(`papel deve ser "ia" ou "admin"`);
  if (b.senha !== undefined && String(b.senha).length < 12) throw new ErroParametro("senha: mínimo de 12 caracteres");

  const existente = (await todos()).find((u) => u.email === emailDe(usuario));
  if (existente) {
    const mudanca: Corpo = {};
    if (b.senha !== undefined) mudanca.password = String(b.senha);
    if (b.papel !== undefined) mudanca.app_metadata = { papel: b.papel };
    if (Object.keys(mudanca).length === 0) throw new ErroParametro("nada para mudar: informe senha e/ou papel");
    await adminAuth("PUT", `/users/${existente.id}`, mudanca);
    return { usuario, papel: b.papel ?? existente.app_metadata?.papel, criado: false };
  }
  if (b.senha === undefined || b.papel === undefined) throw new ErroParametro("usuário novo: informe senha e papel");
  await adminAuth("POST", "/users", {
    email: emailDe(usuario),
    password: String(b.senha),
    email_confirm: true,
    app_metadata: { papel: b.papel },
  });
  return { usuario, papel: b.papel, criado: true };
}

// O token já emitido continua valendo até expirar (no máximo 1 h).
export async function removerUsuario(usuario: string): Promise<void> {
  const existente = (await todos()).find((u) => u.email === emailDe(usuario));
  if (!existente) throw new ErroAuth(404, "usuário não existe");
  await adminAuth("DELETE", `/users/${existente.id}`);
}
