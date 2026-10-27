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

// ArquivoAtualizarAgora é criado pelo comando "atualizar" (fdc.comando) e consumido pelo agente.
const ArquivoAtualizarAgora = "atualizar.agora"

type Config struct {
	Cliente       string        `json:"cliente"`
	Banco         Banco         `json:"banco"`
	Supabase      Supabase      `json:"supabase"`
	Sincronizacao Sincronizacao `json:"sincronizacao"`
	Local         Local         `json:"local"`
	Atualizacao   Atualizacao   `json:"atualizacao"`

	// Pasta do config.json; queries.yaml, estado/ e logs/ ficam nela.
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

// Supabase é a Edge Function "fdc" que recebe os dados (supabase/functions/fdc).
type Supabase struct {
	URL   string `json:"url"`   // https://<projeto>.supabase.co/functions/v1/fdc
	Token string `json:"token"` // o mesmo do secret FDC_TOKEN_WORKER da função
}

type Sincronizacao struct {
	// Tempo máximo de cada SQL no Firebird (a extração de clientes lê a tabela inteira).
	TimeoutSegundos int `json:"timeout_segundos"`
	// Linhas por envio para o Supabase.
	Lote int `json:"lote"`
	// De quanto em quanto tempo compara com o que está no Supabase em vez do estado local
	// (corrige qualquer diferença, inclusive linha apagada à mão lá).
	ReconciliarHoras int `json:"reconciliar_horas"`
}

// Local é o /health só da máquina (127.0.0.1): o agente usa para conferir uma versão nova.
type Local struct {
	Escutar string `json:"escutar"`
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
	if c.Sincronizacao.TimeoutSegundos == 0 {
		c.Sincronizacao.TimeoutSegundos = 120
	}
	if c.Sincronizacao.Lote == 0 {
		c.Sincronizacao.Lote = 500
	}
	if c.Sincronizacao.ReconciliarHoras == 0 {
		c.Sincronizacao.ReconciliarHoras = 24
	}
	if c.Local.Escutar == "" {
		c.Local.Escutar = "127.0.0.1:8080"
	}
	if c.Atualizacao.IntervaloMinutos == 0 {
		c.Atualizacao.IntervaloMinutos = 5
	}
	c.Supabase.URL = strings.TrimRight(c.Supabase.URL, "/")

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
	if !strings.HasPrefix(c.Supabase.URL, "https://") {
		faltando = append(faltando, "supabase.url (https://...)")
	}
	if len(c.Supabase.Token) < 16 {
		faltando = append(faltando, "supabase.token (mínimo 16 caracteres)")
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

// URLLocal é o endereço que o agente usa para falar com o worker na mesma máquina.
func (l Local) URLLocal() string {
	host, porta, err := net.SplitHostPort(l.Escutar)
	if err != nil {
		return "http://" + l.Escutar
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, porta)
}
