// deno test supabase/functions/fdc/params_test.ts
import { converter, ErroParametro, numeroBR, palavras } from "./params.ts";

function igual(a: unknown, b: unknown) {
  if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`${JSON.stringify(a)} != ${JSON.stringify(b)}`);
}

function falha(f: () => unknown) {
  try {
    f();
  } catch (e) {
    if (e instanceof ErroParametro) return;
    throw e;
  }
  throw new Error("deveria dar ErroParametro");
}

Deno.test("palavras: sem acento, sem palavras vazias, até o máximo", () => {
  igual(palavras("Colchão de Casal", 4), ["COLCHAO", "CASAL"]);
  igual(palavras("guarda-roupa 6 portas com espelho", 3), ["GUARDA", "ROUPA", "PORTAS"]);
});

Deno.test("numeroBR", () => {
  const casos: Record<string, number> = { "1500": 1500, "1.500,90": 1500.9, "1500,90": 1500.9, "1500.90": 1500.9, "R$ 2.000": 2000, "1.299.000": 1299000 };
  for (const [s, n] of Object.entries(casos)) igual(numeroBR(s), n);
});

Deno.test("documento, telefone e palavras geram o formato da cópia", () => {
  igual(converter({ nome: "documento", tipo: "documento", obrigatorio: true }, "123.456.789-09"), { documento: "12345678909" });
  igual(converter({ nome: "telefone", tipo: "telefone" }, "+55 (22) 99203-3515"), { telefone: "92033515" });
  igual(converter({ nome: "busca", tipo: "palavras", obrigatorio: true }, "colchão casal"), { busca: "COLCHAO CASAL", busca_termos: "COLCHAO CASAL" });
  falha(() => converter({ nome: "documento", tipo: "documento" }, "123"));
  falha(() => converter({ nome: "telefone", tipo: "telefone" }, "1234"));
  falha(() => converter({ nome: "codigo", tipo: "inteiro", obrigatorio: true }, ""));
  falha(() => converter({ nome: "codigo", tipo: "inteiro" }, "10.5"));
});

Deno.test("fixo ignora o que a IA mandou", () => {
  igual(converter({ nome: "desconto", tipo: "numero", fixo: true, padrao: "5" }, "90"), { desconto: 5 });
});
