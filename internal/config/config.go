// Package config lê o config.json que fica na máquina do cliente, ao lado dos .exe.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ArquivoAtualizarAgora é criado pelo POST /admin/atualizar e consumido pelo agente.
const ArquivoAtualizarAgora = "atualizar.agora"

type Config struct {
	Cliente     string      `json:"cliente"`
	Banco       Banco       `json:"banco"`
	API         API         `json:"api"`
	Atualizacao Atualizacao `json:"atualizacao"`

	// Pasta do config.json; queries.yaml e logs/ ficam nela.
	Dir string `json:"-"`
}

type Banco struct {
	Host    string `json:"host"`
	Porta   int    `json:"porta"`
	Arquivo string `json:"arquivo"` // caminho do .FDB no servidor ou alias
	Usuario string `json:"usuario"`
	Senha   string `json:"senha"`
	Charset string `json:"charset"`
	// Opções extras do driver no formato de URL, ex.: "wire_crypt=false&auth_plugin_name=Legacy_Auth".
	Opcoes string `json:"opcoes"`
}

type API struct {
	Escutar         string `json:"escutar"`
	Token           string `json:"token"`
	TokenAdmin      string `json:"token_admin"`
	SQLLivre        bool   `json:"sql_livre"`
	MaxLinhas       int    `json:"max_linhas"`
	TimeoutSegundos int    `json:"timeout_segundos"`
	MaxSimultaneas  int    `json:"max_simultaneas"`
}

type Atualizacao struct {
	ManifestoURL     string `json:"manifesto_url"`
	ChavePublica     string `json:"chave_publica"` // ed25519, base64
	IntervaloMinutos int    `json:"intervalo_minutos"`
}

func Carregar(caminho string) (*Config, error) {
	b, err := os.ReadFile(caminho)
	if err != nil {
		return nil, err
	}
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")) // BOM do Bloco de Notas / PowerShell 5
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("config.json inválido: %w", err)
	}
	abs, err := filepath.Abs(caminho)
	if err != nil {
		return nil, err
	}
	c.Dir = filepath.Dir(abs)

	if c.Banco.Host == "" {
		c.Banco.Host = "127.0.0.1"
	}
	if c.Banco.Porta == 0 {
		c.Banco.Porta = 3050
	}
	if c.Banco.Charset == "" {
		c.Banco.Charset = "WIN1252"
	}
	if c.API.Escutar == "" {
		c.API.Escutar = "127.0.0.1:8080"
	}
	if c.API.MaxLinhas == 0 {
		c.API.MaxLinhas = 200
	}
	if c.API.TimeoutSegundos == 0 {
		c.API.TimeoutSegundos = 15
	}
	if c.API.MaxSimultaneas == 0 {
		c.API.MaxSimultaneas = 4
	}
	if c.Atualizacao.IntervaloMinutos == 0 {
		c.Atualizacao.IntervaloMinutos = 5
	}

	var faltando []string
	if c.Cliente == "" {
		faltando = append(faltando, "cliente")
	}
	if c.Banco.Arquivo == "" {
		faltando = append(faltando, "banco.arquivo")
	}
	if c.Banco.Usuario == "" {
		faltando = append(faltando, "banco.usuario")
	}
	if len(c.API.Token) < 16 {
		faltando = append(faltando, "api.token (mínimo 16 caracteres)")
	}
	if len(faltando) > 0 {
		return nil, errors.New("config.json incompleto: " + strings.Join(faltando, ", "))
	}
	return &c, nil
}

// DSN monta a string de conexão do driver firebirdsql.
func (b Banco) DSN() string {
	q, _ := url.ParseQuery(b.Opcoes)
	q.Set("charset", b.Charset)
	u := url.URL{
		User:     url.UserPassword(b.Usuario, b.Senha),
		Host:     b.Host + ":" + strconv.Itoa(b.Porta),
		Path:     "/" + strings.ReplaceAll(b.Arquivo, `\`, "/"),
		RawQuery: q.Encode(),
	}
	return strings.TrimPrefix(u.String(), "//")
}

func (c *Config) Caminho(nome string) string {
	return filepath.Join(c.Dir, nome)
}

// URLLocal é o endereço que o agente usa para falar com a API na mesma máquina.
func (a API) URLLocal() string {
	host, porta, err := net.SplitHostPort(a.Escutar)
	if err != nil {
		return "http://" + a.Escutar
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, porta)
}
