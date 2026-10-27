// Package consultas carrega o queries.yaml: as extrações (sincronizar), que o worker copia
// para o Supabase, e as consultas de diagnóstico, com parâmetros nomeados (:nome) e tipados.
package consultas

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"ponte/internal/texto"
)

type Arquivo struct {
	Sincronizar map[string]*Extracao `yaml:"sincronizar"`
	Consultas   map[string]*Consulta `yaml:"consultas"`
}

// Extracao é uma tabela copiada para o Supabase. O SQL roda inteiro a cada intervalo e o
// worker manda só as linhas que mudaram (compara o hash de cada linha). As colunas do SQL
// têm o mesmo nome das colunas da tabela fdc.<nome> (supabase/migrations).
type Extracao struct {
	Tabela    string `yaml:"-"`
	Descricao string `yaml:"descricao"`
	// Colunas do SQL que identificam a linha no ERP; viram a "chave" no Supabase ("14386|01").
	Chave            []string `yaml:"chave"`
	IntervaloMinutos int      `yaml:"intervalo_minutos"`
	// Se for apagar mais que isso (% das linhas já enviadas), o worker não apaga nada e
	// avisa: protege a cópia de um SQL quebrado ou de um banco restaurado de backup antigo.
	MaxRemocaoPerc int `yaml:"max_remocao_perc"`
	// Tipo "foto": cada linha é uma imagem; sql_arquivo busca o binário por :codigo e :seq.
	Tipo       string `yaml:"tipo"`
	SQL        string `yaml:"sql"`
	SQLArquivo string `yaml:"sql_arquivo"`

	Arquivo *Consulta `yaml:"-"`
}

// Consulta de diagnóstico: roda quando a equipe pede pelo Supabase (fdc.comando) ou no -testar.
type Consulta struct {
	Nome      string   `yaml:"-"`
	Descricao string   `yaml:"descricao"`
	MaxLinhas int      `yaml:"max_linhas"`
	Params    []*Param `yaml:"params"`
	SQL       string   `yaml:"sql"`

	sqlPosicional string   // SQL com ? no lugar de :nome
	ordem         []string // nome de cada ?, na ordem em que aparece
}

type Param struct {
	Nome        string `yaml:"nome" json:"nome"`
	Tipo        string `yaml:"tipo" json:"tipo"`
	Descricao   string `yaml:"descricao" json:"descricao,omitempty"`
	Obrigatorio bool   `yaml:"obrigatorio" json:"obrigatorio"`
	Padrao      string `yaml:"padrao" json:"padrao,omitempty"`
	// Exemplo é usado pelo "worker-fdc -testar".
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
	Extracoes map[string]*Extracao
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
	if err := yaml.Unmarshal(bytes.TrimPrefix(conteudo, []byte("\xef\xbb\xbf")), &a); err != nil {
		return nil, fmt.Errorf("YAML inválido: %w", err)
	}
	if len(a.Sincronizar) == 0 {
		return nil, fmt.Errorf("nenhuma extração em sincronizar")
	}
	for nome, e := range a.Sincronizar {
		if e == nil {
			return nil, fmt.Errorf("extração %s vazia", nome)
		}
		e.Tabela = nome
		if err := e.preparar(); err != nil {
			return nil, fmt.Errorf("extração %s: %w", nome, err)
		}
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
	return &Conjunto{SHA256: hex.EncodeToString(soma[:]), Extracoes: a.Sincronizar, Consultas: a.Consultas}, nil
}

func (e *Extracao) preparar() error {
	if err := soSelect(e.SQL); err != nil {
		return err
	}
	if _, ordem, err := nomeadosParaPosicionais(e.SQL); err != nil {
		return err
	} else if len(ordem) > 0 {
		return fmt.Errorf("o SQL de extração não aceita parâmetros (:%s)", ordem[0])
	}
	if len(e.Chave) == 0 {
		return fmt.Errorf("chave vazia")
	}
	for i := range e.Chave {
		e.Chave[i] = strings.ToLower(e.Chave[i])
	}
	if e.IntervaloMinutos <= 0 {
		e.IntervaloMinutos = 10
	}
	if e.MaxRemocaoPerc <= 0 {
		e.MaxRemocaoPerc = 30
	}
	switch e.Tipo {
	case "":
	case "foto":
		if strings.Join(e.Chave, ",") != "codigo,seq" {
			return fmt.Errorf("tipo foto exige chave [codigo, seq]")
		}
		e.Arquivo = &Consulta{
			Nome: e.Tabela + ".sql_arquivo",
			SQL:  e.SQLArquivo,
			Params: []*Param{
				{Nome: "codigo", Tipo: "inteiro", Obrigatorio: true},
				{Nome: "seq", Tipo: "inteiro", Obrigatorio: true},
			},
		}
		if err := e.Arquivo.preparar(); err != nil {
			return fmt.Errorf("sql_arquivo: %w", err)
		}
	default:
		return fmt.Errorf("tipo %q desconhecido (use foto ou deixe vazio)", e.Tipo)
	}
	return nil
}

func soSelect(sql string) error {
	if strings.TrimSpace(sql) == "" {
		return fmt.Errorf("sql vazio")
	}
	inicio := strings.ToUpper(strings.TrimSpace(sql))
	if !strings.HasPrefix(inicio, "SELECT") && !strings.HasPrefix(inicio, "WITH") {
		return fmt.Errorf("só SELECT é permitido")
	}
	return nil
}

func (c *Consulta) preparar() error {
	if err := soSelect(c.SQL); err != nil {
		return err
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

var milharBR = regexp.MustCompile(`^\d{1,3}(\.\d{3})+$`)

// numeroBR aceita "1500", "1500.90", "1.500,90", "1500,90" e "2.000" (= dois mil).
func numeroBR(s string) (float64, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.ToUpper(strings.ReplaceAll(s, " ", "")), "R$"))
	if strings.Contains(s, ",") || milharBR.MatchString(s) {
		s = strings.ReplaceAll(s, ".", "")
		s = strings.ReplaceAll(s, ",", ".")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("inválido")
	}
	return f, nil
}
