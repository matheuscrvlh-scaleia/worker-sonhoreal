// Package texto normaliza o que chega do WhatsApp (CPF, telefone, termos de busca)
// para o formato que o ERP grava.
package texto

import (
	"strings"
	"unicode"
)

// Digitos mantém só os números: "(11) 98765-4321" -> "11987654321".
func Digitos(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// FormatarDocumento aplica a máscara de CPF (11 dígitos) ou CNPJ (14 dígitos).
// Qualquer outro tamanho volta como veio.
func FormatarDocumento(d string) string {
	switch len(d) {
	case 11:
		return d[0:3] + "." + d[3:6] + "." + d[6:9] + "-" + d[9:11]
	case 14:
		return d[0:2] + "." + d[2:5] + "." + d[5:8] + "/" + d[8:12] + "-" + d[12:14]
	}
	return d
}

// Telefone limpa o número e tira o DDI 55 quando vem do WhatsApp
// ("5511987654321" -> "11987654321").
func Telefone(s string) string {
	d := Digitos(s)
	if len(d) >= 12 && strings.HasPrefix(d, "55") {
		d = d[2:]
	}
	return d
}

// Final8 devolve os últimos 8 dígitos. É o pedaço que sobrevive a qualquer forma
// de cadastro: com ou sem DDD, com ou sem o 9 na frente.
func Final8(tel string) string {
	if len(tel) <= 8 {
		return tel
	}
	return tel[len(tel)-8:]
}

var acentos = map[rune]rune{
	'Á': 'A', 'À': 'A', 'Â': 'A', 'Ã': 'A', 'Ä': 'A',
	'É': 'E', 'È': 'E', 'Ê': 'E', 'Ë': 'E',
	'Í': 'I', 'Ì': 'I', 'Î': 'I', 'Ï': 'I',
	'Ó': 'O', 'Ò': 'O', 'Ô': 'O', 'Õ': 'O', 'Ö': 'O',
	'Ú': 'U', 'Ù': 'U', 'Û': 'U', 'Ü': 'U',
	'Ç': 'C', 'Ñ': 'N',
}

// SemAcento deixa em maiúsculas e sem acento, como a maioria dos ERPs grava a descrição.
func SemAcento(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if t, ok := acentos[r]; ok {
			r = t
		}
		b.WriteRune(r)
	}
	return b.String()
}

var palavrasVazias = map[string]bool{
	"DE": true, "DA": true, "DO": true, "DAS": true, "DOS": true, "COM": true, "SEM": true,
	"PARA": true, "PRA": true, "UM": true, "UMA": true, "O": true, "A": true, "OS": true,
	"AS": true, "E": true, "EM": true, "NO": true, "NA": true, "TEM": true, "VOCES": true,
}

// Palavras quebra a busca em termos úteis: "colchão de casal" -> [COLCHAO CASAL].
func Palavras(s string, removerAcento bool, max int) []string {
	if removerAcento {
		s = SemAcento(s)
	} else {
		s = strings.ToUpper(s)
	}
	campos := strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '/' && r != '.' && r != ','
	})
	var out []string
	for _, c := range campos {
		if len(out) == max {
			break
		}
		if len([]rune(c)) < 2 || palavrasVazias[c] {
			continue
		}
		out = append(out, c)
	}
	return out
}
