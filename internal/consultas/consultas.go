// Package consultas carrega o queries.yaml: cada consulta é um SQL com parâmetros
// nomeados (:nome) e tipados, exposto como GET/POST /q/{nome}.
package consultas

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"ponte/internal/texto"
)

type Arquivo struct {
	Consultas map[string]*Consulta `yaml:"consultas"`
}

type Consulta struct {
	Nome      string   `yaml:"-" json:"nome"`
	Descricao string   `yaml:"descricao" json:"descricao"`
	Admin     bool     `yaml:"admin" json:"-"`
	MaxLinhas int      `yaml:"max_linhas" json:"-"`
	Params    []*Param `yaml:"params" json:"params"`
	SQL       string   `yaml:"sql" json:"-"`

	sqlPosicional string   // SQL com ? no lugar de :nome
	ordem         []string // nome de cada ?, na ordem em que aparece
}

type Param struct {
	Nome        string `yaml:"nome" json:"nome"`
	Tipo        string `yaml:"tipo" json:"tipo"`
	Descricao   string `yaml:"descricao" json:"descricao,omitempty"`
	Obrigatorio bool   `yaml:"obrigatorio" json:"obrigatorio"`
	Padrao      string `yaml:"padrao" json:"padrao,omitempty"`
	// Exemplo aparece na listagem para a IA e é usado pelo "ponte-api -testar".
	Exemplo string `yaml:"exemplo" json:"exemplo,omitempty"`
	// Fixo: quem chama não consegue mudar, vale sempre o padrão (ex.: desconto máximo da loja).
	Fixo bool `yaml:"fixo" json:"-"`
	// Só para tipo palavras.
	MaxPalavras   int  `yaml:"max_palavras" json:"-"`
	ManterAcentos bool `yaml:"manter_acentos" json:"-"`
}

// Conjunto é o queries.yaml já validado e pronto para uso.
type Conjunto struct {
	SHA256    string
	Consultas map[string]*Consulta
}

var tiposValidos = map[string]bool{
	"texto": true, "inteiro": true, "numero": true, "data": true,
	"documento": true, "telefone": true, "palavras": true,
}

// Carregar valida o YAML inteiro; qualquer erro invalida o arquivo todo
// (assim um deploy quebrado nunca substitui um que funciona).
func Carregar(conteudo []byte) (*Conjunto, error) {
	var a Arquivo
	if err := yaml.Unmarshal(conteudo, &a); err != nil {
		return nil, fmt.Errorf("YAML inválido: %w", err)
	}
	if len(a.Consultas) == 0 {
		return nil, fmt.Errorf("nenhuma consulta definida")
	}
	for nome, c := range a.Consultas {
		if c == nil {
			return nil, fmt.Errorf("consulta %s vazia", nome)
		}
		c.Nome = nome
		if err := c.preparar(); err != nil {
			return nil, fmt.Errorf("consulta %s: %w", nome, err)
		}
	}
	soma := sha256.Sum256(conteudo)
	return &Conjunto{SHA256: hex.EncodeToString(soma[:]), Consultas: a.Consultas}, nil
}

func (c *Consulta) preparar() error {
	if strings.TrimSpace(c.SQL) == "" {
		return fmt.Errorf("sql vazio")
	}
	inicio := strings.ToUpper(strings.TrimSpace(c.SQL))
	if !strings.HasPrefix(inicio, "SELECT") && !strings.HasPrefix(inicio, "WITH") {
		return fmt.Errorf("só SELECT é permitido")
	}
	disponiveis := map[string]bool{}
	for _, p := range c.Params {
		if !tiposValidos[p.Tipo] {
			return fmt.Errorf("parâmetro %s: tipo %q desconhecido", p.Nome, p.Tipo)
		}
		if p.Tipo == "palavras" && p.MaxPalavras == 0 {
			p.MaxPalavras = 4
		}
		if p.Fixo && p.Padrao == "" {
			return fmt.Errorf("parâmetro %s: fixo precisa de padrao", p.Nome)
		}
		for _, n := range p.nomesGerados() {
			disponiveis[n] = true
		}
	}
	sqlPos, ordem, err := nomeadosParaPosicionais(c.SQL)
	if err != nil {
		return err
	}
	for _, n := range ordem {
		if !disponiveis[n] {
			return fmt.Errorf("o SQL usa :%s, que não é declarado em params", n)
		}
	}
	c.sqlPosicional, c.ordem = sqlPos, ordem
	return nil
}

// nomesGerados lista os :nomes que cada parâmetro disponibiliza no SQL.
func (p *Param) nomesGerados() []string {
	switch p.Tipo {
	case "documento":
		return []string{p.Nome, p.Nome + "_formatado"}
	case "telefone":
		return []string{p.Nome, p.Nome + "_final8"}
	case "palavras":
		n := []string{p.Nome}
		for i := 1; i <= p.MaxPalavras; i++ {
			n = append(n, p.Nome+"_"+strconv.Itoa(i))
		}
		return n
	}
	return []string{p.Nome}
}

// nomeadosParaPosicionais troca :nome por ?, ignorando strings, identificadores
// entre aspas e comentários.
func nomeadosParaPosicionais(sql string) (string, []string, error) {
	var out strings.Builder
	var ordem []string
	r := []rune(sql)
	for i := 0; i < len(r); i++ {
		ch := r[i]
		switch {
		case ch == '\'' || ch == '"':
			j := i + 1
			for j < len(r) {
				if r[j] == ch {
					if j+1 < len(r) && r[j+1] == ch { // aspas escapadas ('')
						j += 2
						continue
					}
					break
				}
				j++
			}
			if j >= len(r) {
				return "", nil, fmt.Errorf("aspas sem fechar no SQL")
			}
			out.WriteString(string(r[i : j+1]))
			i = j
		case ch == '-' && i+1 < len(r) && r[i+1] == '-':
			for i < len(r) && r[i] != '\n' {
				i++
			}
			out.WriteRune('\n')
		case ch == ':' && i+1 < len(r) && (isLetra(r[i+1]) || r[i+1] == '_'):
			j := i + 1
			for j < len(r) && (isLetra(r[j]) || r[j] == '_' || (r[j] >= '0' && r[j] <= '9')) {
				j++
			}
			ordem = append(ordem, strings.ToLower(string(r[i+1:j])))
			out.WriteRune('?')
			i = j - 1
		default:
			out.WriteRune(ch)
		}
	}
	return out.String(), ordem, nil
}

func isLetra(r rune) bool { return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') }

// ErroParametro é culpa de quem chamou (HTTP 400), não do banco.
type ErroParametro struct{ Msg string }

func (e *ErroParametro) Error() string { return e.Msg }

func erroParam(format string, a ...any) error {
	return &ErroParametro{Msg: fmt.Sprintf(format, a...)}
}

// Montar converte os valores recebidos no SQL posicional + argumentos.
func (c *Consulta) Montar(recebidos map[string]string) (string, []any, error) {
	valores := map[string]any{}
	for _, p := range c.Params {
		bruto, veio := recebidos[p.Nome]
		bruto = strings.TrimSpace(bruto)
		if p.Fixo || !veio || bruto == "" {
			bruto = p.Padrao
		}
		if bruto == "" && p.Obrigatorio {
			return "", nil, erroParam("parâmetro obrigatório: %s", p.Nome)
		}
		if err := p.converter(bruto, valores); err != nil {
			return "", nil, err
		}
	}
	args := make([]any, len(c.ordem))
	for i, n := range c.ordem {
		args[i] = valores[n]
	}
	return c.sqlPosicional, args, nil
}

func (p *Param) converter(bruto string, valores map[string]any) error {
	if len(bruto) > 300 {
		return erroParam("%s: valor grande demais", p.Nome)
	}
	switch p.Tipo {
	case "texto":
		valores[p.Nome] = bruto
	case "inteiro":
		if bruto == "" {
			valores[p.Nome] = nil
			return nil
		}
		n, err := strconv.ParseInt(texto.Digitos(bruto), 10, 64)
		if err != nil || strings.ContainsAny(bruto, ".,") {
			return erroParam("%s: esperado número inteiro, veio %q", p.Nome, bruto)
		}
		valores[p.Nome] = n
	case "numero":
		if bruto == "" {
			valores[p.Nome] = nil
			return nil
		}
		f, err := numeroBR(bruto)
		if err != nil {
			return erroParam("%s: esperado número, veio %q", p.Nome, bruto)
		}
		valores[p.Nome] = f
	case "data":
		if bruto == "" {
			valores[p.Nome] = nil
			return nil
		}
		var t time.Time
		var err error
		for _, layout := range []string{"2006-01-02", "02/01/2006"} {
			if t, err = time.ParseInLocation(layout, bruto, time.Local); err == nil {
				break
			}
		}
		if err != nil {
			return erroParam("%s: data inválida %q (use AAAA-MM-DD)", p.Nome, bruto)
		}
		valores[p.Nome] = t
	case "documento":
		d := texto.Digitos(bruto)
		if bruto != "" && len(d) != 11 && len(d) != 14 {
			return erroParam("%s: CPF/CNPJ deve ter 11 ou 14 dígitos", p.Nome)
		}
		valores[p.Nome] = d
		valores[p.Nome+"_formatado"] = texto.FormatarDocumento(d)
	case "telefone":
		t := texto.Telefone(bruto)
		if bruto != "" && len(t) < 8 {
			return erroParam("%s: telefone precisa de pelo menos 8 dígitos", p.Nome)
		}
		valores[p.Nome] = t
		valores[p.Nome+"_final8"] = texto.Final8(t)
	case "palavras":
		ws := texto.Palavras(bruto, !p.ManterAcentos, p.MaxPalavras)
		if p.Obrigatorio && len(ws) == 0 {
			return erroParam("%s: nenhum termo de busca útil", p.Nome)
		}
		inteiro := strings.TrimSpace(strings.ToUpper(bruto))
		if !p.ManterAcentos {
			inteiro = texto.SemAcento(inteiro)
		}
		valores[p.Nome] = inteiro
		// Termos que sobram ficam '' e "CONTAINING ''" é sempre verdadeiro.
		for i := 1; i <= p.MaxPalavras; i++ {
			v := ""
			if i <= len(ws) {
				v = ws[i-1]
			}
			valores[p.Nome+"_"+strconv.Itoa(i)] = v
		}
	}
	return nil
}

// numeroBR aceita "1500", "1500.90", "1.500,90" e "1500,90".
func numeroBR(s string) (float64, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.ToUpper(strings.ReplaceAll(s, " ", "")), "R$"))
	if strings.Contains(s, ",") {
		s = strings.ReplaceAll(s, ".", "")
		s = strings.ReplaceAll(s, ",", ".")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("inválido")
	}
	return f, nil
}

// Publicas lista as consultas que a IA pode usar, em ordem alfabética.
func (cj *Conjunto) Publicas() []*Consulta {
	var out []*Consulta
	for _, c := range cj.Consultas {
		if !c.Admin {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Nome < out[j].Nome })
	return out
}
