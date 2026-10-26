package texto

import (
	"reflect"
	"testing"
)

func TestTelefone(t *testing.T) {
	casos := map[string][2]string{ // entrada -> {Telefone, Final8}
		"5511987654321":       {"11987654321", "87654321"},
		"+55 (11) 98765-4321": {"11987654321", "87654321"},
		"(11) 3333-4444":      {"1133334444", "33334444"},
		"98765-4321":          {"987654321", "87654321"},
	}
	for in, want := range casos {
		tel := Telefone(in)
		if tel != want[0] || Final8(tel) != want[1] {
			t.Errorf("%q -> %q/%q, want %v", in, tel, Final8(tel), want)
		}
	}
}

func TestFormatarDocumento(t *testing.T) {
	if got := FormatarDocumento("12345678909"); got != "123.456.789-09" {
		t.Error(got)
	}
	if got := FormatarDocumento("12345678000195"); got != "12.345.678/0001-95" {
		t.Error(got)
	}
}

func TestPalavras(t *testing.T) {
	got := Palavras("Vocês têm colchão de casal 1,38 Ortobom?", true, 4)
	want := []string{"COLCHAO", "CASAL", "1,38", "ORTOBOM"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
