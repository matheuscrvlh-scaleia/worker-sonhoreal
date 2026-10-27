// Normaliza o que chega da IA (CPF, telefone, termos de busca, valores em reais) para o
// formato gravado na cópia do ERP. Mesmas regras do internal/texto do worker.

export type Tipo = "texto" | "inteiro" | "numero" | "data" | "documento" | "telefone" | "palavras";

export interface Param {
  nome: string;
  tipo: Tipo;
  obrigatorio?: boolean;
  padrao?: string;
  descricao?: string;
  exemplo?: string;
  // A IA não consegue mudar: vale sempre o padrão (ex.: desconto máximo da loja).
  fixo?: boolean;
  max_palavras?: number;
}

// Culpa de quem chamou: vira HTTP 400.
export class ErroParametro extends Error {}

export const digitos = (s: string) => s.replace(/\D/g, "");

// Tira o DDI 55 quando vem do WhatsApp ("5511987654321" -> "11987654321").
export function telefone(s: string): string {
  const d = digitos(s);
  return d.length >= 12 && d.startsWith("55") ? d.slice(2) : d;
}

// Os últimos 8 dígitos sobrevivem a qualquer forma de cadastro (com ou sem DDD, com ou sem o 9).
export const final8 = (t: string) => (t.length <= 8 ? t : t.slice(-8));

export const semAcento = (s: string) => s.toUpperCase().normalize("NFD").replace(/[̀-ͯ]/g, "");

const vazias = new Set([
  "DE", "DA", "DO", "DAS", "DOS", "COM", "SEM", "PARA", "PRA", "UM", "UMA", "O", "A", "OS",
  "AS", "E", "EM", "NO", "NA", "TEM", "VOCES",
]);

// "colchão de casal" -> ["COLCHAO", "CASAL"]
export function palavras(s: string, max: number): string[] {
  const out: string[] = [];
  for (const c of semAcento(s).split(/[^\p{L}\p{N}/.,]+/u)) {
    if (out.length === max) break;
    if ([...c].length < 2 || vazias.has(c)) continue;
    out.push(c);
  }
  return out;
}

// Aceita "1500", "1500.90", "1.500,90", "1500,90", "R$ 2.000" (= dois mil).
export function numeroBR(s: string): number {
  let t = s.replace(/\s/g, "").toUpperCase().replace(/^R\$/, "");
  if (t.includes(",") || /^\d{1,3}(\.\d{3})+$/.test(t)) t = t.replaceAll(".", "").replace(",", ".");
  const n = Number(t);
  if (t === "" || !Number.isFinite(n)) throw new Error("inválido");
  return n;
}

// converter devolve os valores que o parâmetro gera para a função SQL. Tipo palavras gera
// dois: {nome} (texto inteiro, para código/EAN) e {nome}_termos (termos separados por espaço).
export function converter(p: Param, recebido: string | undefined): Record<string, string | number | null> {
  let bruto = (recebido ?? "").trim();
  if (p.fixo || bruto === "") bruto = p.padrao ?? "";
  if (bruto === "" && p.obrigatorio) throw new ErroParametro(`parâmetro obrigatório: ${p.nome}`);
  if (bruto.length > 300) throw new ErroParametro(`${p.nome}: valor grande demais`);

  switch (p.tipo) {
    case "texto":
      return { [p.nome]: bruto };
    case "inteiro": {
      if (bruto === "") return { [p.nome]: null };
      if (/[.,]/.test(bruto) || digitos(bruto) === "") {
        throw new ErroParametro(`${p.nome}: esperado número inteiro, veio "${bruto}"`);
      }
      return { [p.nome]: digitos(bruto) };
    }
    case "numero": {
      if (bruto === "") return { [p.nome]: null };
      try {
        return { [p.nome]: numeroBR(bruto) };
      } catch {
        throw new ErroParametro(`${p.nome}: esperado número, veio "${bruto}"`);
      }
    }
    case "data": {
      if (bruto === "") return { [p.nome]: null };
      let m = bruto.match(/^(\d{4})-(\d{2})-(\d{2})$/);
      if (m) return { [p.nome]: `${m[1]}-${m[2]}-${m[3]}` };
      m = bruto.match(/^(\d{2})\/(\d{2})\/(\d{4})$/);
      if (m) return { [p.nome]: `${m[3]}-${m[2]}-${m[1]}` };
      throw new ErroParametro(`${p.nome}: data inválida "${bruto}" (use AAAA-MM-DD)`);
    }
    case "documento": {
      const d = digitos(bruto);
      if (bruto !== "" && d.length !== 11 && d.length !== 14) {
        throw new ErroParametro(`${p.nome}: CPF/CNPJ deve ter 11 ou 14 dígitos`);
      }
      return { [p.nome]: d };
    }
    case "telefone": {
      const t = telefone(bruto);
      if (bruto !== "" && t.length < 8) {
        throw new ErroParametro(`${p.nome}: telefone precisa de pelo menos 8 dígitos`);
      }
      return { [p.nome]: final8(t) };
    }
    case "palavras": {
      const ws = palavras(bruto, p.max_palavras ?? 4);
      if (p.obrigatorio && ws.length === 0) throw new ErroParametro(`${p.nome}: nenhum termo de busca útil`);
      return { [p.nome]: semAcento(bruto), [`${p.nome}_termos`]: ws.join(" ") };
    }
  }
}
