package consultas

import (
	"reflect"
	"strings"
	"testing"
)

const yamlTeste = `
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

func TestRecusaArquivoRuim(t *testing.T) {
	ruins := map[string]string{
		"param não declarado": "consultas:\n  x:\n    sql: SELECT 1 FROM T WHERE A = :nada\n",
		"não é SELECT":        "consultas:\n  x:\n    sql: DELETE FROM T\n",
		"tipo inválido":       "consultas:\n  x:\n    params: [{nome: a, tipo: bool}]\n    sql: SELECT :a FROM T\n",
		"fixo sem padrão":     "consultas:\n  x:\n    params: [{nome: a, tipo: texto, fixo: true}]\n    sql: SELECT :a FROM T\n",
		"vazio":               "consultas: {}\n",
	}
	for nome, y := range ruins {
		if _, err := Carregar([]byte(y)); err == nil {
			t.Errorf("%s: deveria recusar", nome)
		}
	}
}
