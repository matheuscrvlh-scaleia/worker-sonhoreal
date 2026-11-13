package consultas

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

const yamlTeste = `
sincronizar:
  estoque:
    chave: [CODIGO, filial]
    sql: SELECT IDPRODUTO AS codigo, IDFILIAL AS filial, ':x' AS y FROM ESTOQUE
  foto:
    tipo: foto
    chave: [codigo, seq]
    intervalo_minutos: 60
    sql: SELECT IDPRODUTO AS codigo, SEQ AS seq FROM PRODUTOFOTO
    sql_arquivo: SELECT FOTO FROM PRODUTOFOTO WHERE IDPRODUTO = :codigo AND SEQ = :seq
consultas:
  busca:
    params:
      - {nome: busca, tipo: palavras, obrigatorio: true, max_palavras: 3}
      - {nome: filial, tipo: texto, padrao: "01"}
      - {nome: limite, tipo: numero, fixo: true, padrao: "5"}
    sql: |
      SELECT ':nao_e_param' AS x, "COL:ID" -- :comentario
      FROM T WHERE A CONTAINING :busca_1 AND B CONTAINING :busca_2 AND C CONTAINING :busca_3
        AND F = :filial AND L < :limite AND D = :busca
  cliente:
    params:
      - {nome: doc, tipo: documento, obrigatorio: true}
      - {nome: tel, tipo: telefone}
    sql: SELECT 1 FROM C WHERE D IN (:doc, :doc_formatado) OR T CONTAINING :tel_final8
`

func carregar(t *testing.T) *Conjunto {
	t.Helper()
	cj, err := Carregar([]byte(yamlTeste))
	if err != nil {
		t.Fatal(err)
	}
	return cj
}

func TestPosicionais(t *testing.T) {
	c := carregar(t).Consultas["busca"]
	sql, args, err := c.Montar(map[string]string{"busca": "Colchão de Casal", "limite": "99"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(sql, "?") != 6 {
		t.Errorf("esperava 6 '?', SQL: %s", sql)
	}
	if !strings.Contains(sql, "':nao_e_param'") || !strings.Contains(sql, `"COL:ID"`) {
		t.Errorf("mexeu dentro de string/identificador: %s", sql)
	}
	if strings.Contains(sql, "comentario") {
		t.Errorf("comentário não foi removido: %s", sql)
	}
	// "de" é palavra vazia; termo que sobra vira ''; limite é fixo e ignora o 99.
	want := []any{"COLCHAO", "CASAL", "", "01", 5.0, "COLCHAO DE CASAL"}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("args = %#v, want %#v", args, want)
	}
}

func TestDocumentoETelefone(t *testing.T) {
	c := carregar(t).Consultas["cliente"]
	_, args, err := c.Montar(map[string]string{"doc": "123.456.789-09", "tel": "+55 (11) 98765-4321"})
	if err != nil {
		t.Fatal(err)
	}
	want := []any{"12345678909", "123.456.789-09", "87654321"}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("args = %#v, want %#v", args, want)
	}
	if _, _, err := c.Montar(map[string]string{"doc": "123"}); err == nil {
		t.Error("CPF com 3 dígitos deveria dar erro")
	}
	if _, _, err := c.Montar(map[string]string{}); err == nil {
		t.Error("documento obrigatório ausente deveria dar erro")
	}
}

func TestNumeroBR(t *testing.T) {
	for in, want := range map[string]float64{"1500": 1500, "1.500,90": 1500.9, "1500,90": 1500.9, "1500.90": 1500.9, "R$ 2.000": 2000, "R$ 2.000,00": 2000, "1.299.000": 1299000, "0.5": 0.5} {
		got, err := numeroBR(in)
		if err != nil || got != want {
			t.Errorf("numeroBR(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
}

func TestExtracoes(t *testing.T) {
	cj := carregar(t)
	e := cj.Extracoes["estoque"]
	if e.Tabela != "estoque" || e.IntervaloMinutos != 10 || e.MaxRemocaoPerc != 30 {
		t.Errorf("padrões errados: %+v", e)
	}
	if !reflect.DeepEqual(e.Chave, []string{"codigo", "filial"}) {
		t.Errorf("chave = %v (deveria vir em minúsculas)", e.Chave)
	}
	f := cj.Extracoes["foto"]
	_, args, err := f.Arquivo.Montar(map[string]string{"codigo": "14386", "seq": "2"})
	if err != nil || !reflect.DeepEqual(args, []any{int64(14386), int64(2)}) {
		t.Errorf("sql_arquivo: args = %#v, err = %v", args, err)
	}
}

func TestRecusaArquivoRuim(t *testing.T) {
	const ok = "sincronizar:\n  e:\n    chave: [a]\n    sql: SELECT 1 AS a FROM T\n"
	ruins := map[string]string{
		"param não declarado":    ok + "consultas:\n  x:\n    sql: SELECT 1 FROM T WHERE A = :nada\n",
		"não é SELECT":           ok + "consultas:\n  x:\n    sql: DELETE FROM T\n",
		"tipo inválido":          ok + "consultas:\n  x:\n    params: [{nome: a, tipo: bool}]\n    sql: SELECT :a FROM T\n",
		"fixo sem padrão":        ok + "consultas:\n  x:\n    params: [{nome: a, tipo: texto, fixo: true}]\n    sql: SELECT :a FROM T\n",
		"sem extração":           "consultas: {}\n",
		"extração sem chave":     "sincronizar:\n  e:\n    sql: SELECT 1 FROM T\n",
		"extração com parâmetro": "sincronizar:\n  e:\n    chave: [a]\n    sql: SELECT 1 AS a FROM T WHERE B = :b\n",
		"extração não é SELECT":  "sincronizar:\n  e:\n    chave: [a]\n    sql: UPDATE T SET A = 1\n",
		"foto sem sql_arquivo":   "sincronizar:\n  e:\n    tipo: foto\n    chave: [codigo, seq]\n    sql: SELECT 1 AS codigo, 1 AS seq FROM T\n",
		"foto com outra chave":   "sincronizar:\n  e:\n    tipo: foto\n    chave: [id]\n    sql: SELECT 1 AS id FROM T\n    sql_arquivo: SELECT F FROM T\n",
	}
	for nome, y := range ruins {
		if _, err := Carregar([]byte(y)); err == nil {
			t.Errorf("%s: deveria recusar", nome)
		}
	}
}

// O queries.yaml do repositório é o que vai para o cliente: tem que carregar.
func TestQueriesDoRepositorio(t *testing.T) {
	conteudo, err := os.ReadFile("../../queries.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cj, err := Carregar(conteudo)
	if err != nil {
		t.Fatal(err)
	}
	// promocao e condicao_fator saíram de propósito (13/11/2026): tabelas vazias ou não usadas na
	// Sonho Real e sem GRANT para o FDCRELATORIO. fdc.promocao e fdc.condicao_fator ficam paradas.
	for _, nome := range []string{"produto", "estoque", "foto", "condicao", "loja", "cliente", "parcela", "compra"} {
		if cj.Extracoes[nome] == nil {
			t.Errorf("falta a extração %s (a tabela fdc.%s existe no Supabase)", nome, nome)
		}
	}
}
