// ponte-assinar: gera as chaves e o manifesto assinado. Usado pelo GitHub Actions.
//
//	ponte-assinar chaves
//	    gera um par novo. PRIVADA vai no secret PONTE_CHAVE_PRIVADA do GitHub,
//	    PUBLICA vai no config.json de cada cliente (atualizacao.chave_publica).
//
//	PONTE_CHAVE_PRIVADA=... ponte-assinar manifesto -cliente sonho-real \
//	    -api dist/ponte-api.exe -api-url ../bin/ponte-api-v1.2.0.exe -api-versao v1.2.0 \
//	    -consultas queries.yaml -consultas-url consultas-<sha>.yaml -saida dist/manifest.json
//	    grava manifest.json e manifest.json.sig
//
//	ponte-assinar verificar -manifesto manifest.json -chave-publica <base64>
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"ponte/internal/atualizacao"
)

func main() {
	if len(os.Args) < 2 {
		uso()
	}
	switch os.Args[1] {
	case "chaves":
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		falhar(err)
		fmt.Println("PRIVADA (secret PONTE_CHAVE_PRIVADA, nunca no repositório):")
		fmt.Println(base64.StdEncoding.EncodeToString(priv.Seed()))
		fmt.Println()
		fmt.Println("PUBLICA (config.json -> atualizacao.chave_publica):")
		fmt.Println(base64.StdEncoding.EncodeToString(pub))
	case "manifesto":
		manifesto(os.Args[2:])
	case "verificar":
		verificar(os.Args[2:])
	default:
		uso()
	}
}

func manifesto(args []string) {
	fs := flag.NewFlagSet("manifesto", flag.ExitOnError)
	cliente := fs.String("cliente", "", "nome do cliente (igual ao config.json)")
	apiExe := fs.String("api", "", "caminho do ponte-api.exe compilado")
	apiURL := fs.String("api-url", "", "URL do .exe, relativa ao manifesto ou absoluta")
	apiVersao := fs.String("api-versao", "", "versão gravada no .exe (-X ponte/internal/versao.Versao)")
	consultas := fs.String("consultas", "", "caminho do queries.yaml")
	consultasURL := fs.String("consultas-url", "", "URL do queries.yaml publicado")
	saida := fs.String("saida", "manifest.json", "arquivo de saída")
	fs.Parse(args)
	if *cliente == "" {
		falhar(fmt.Errorf("-cliente é obrigatório"))
	}

	priv := chavePrivada()
	m := atualizacao.Manifesto{Cliente: *cliente, GeradoEm: time.Now().UTC()}
	if *apiExe != "" {
		sha, err := atualizacao.SHA256Arquivo(*apiExe)
		falhar(err)
		m.API = atualizacao.Componente{Versao: *apiVersao, URL: *apiURL, SHA256: sha}
	}
	if *consultas != "" {
		sha, err := atualizacao.SHA256Arquivo(*consultas)
		falhar(err)
		m.Consultas = atualizacao.Componente{Versao: *apiVersao, URL: *consultasURL, SHA256: sha}
	}
	b, err := json.MarshalIndent(m, "", "  ")
	falhar(err)
	falhar(os.WriteFile(*saida, b, 0o644))
	falhar(os.WriteFile(*saida+".sig", []byte(atualizacao.Assinar(priv, b)), 0o644))
	fmt.Printf("%s e %s.sig gravados\n%s\n", *saida, *saida, b)
}

func verificar(args []string) {
	fs := flag.NewFlagSet("verificar", flag.ExitOnError)
	arq := fs.String("manifesto", "manifest.json", "")
	chave := fs.String("chave-publica", "", "base64")
	fs.Parse(args)
	pub, err := atualizacao.ChavePublica(*chave)
	falhar(err)
	b, err := os.ReadFile(*arq)
	falhar(err)
	sig, err := os.ReadFile(*arq + ".sig")
	falhar(err)
	if !atualizacao.Verificar(pub, b, string(sig)) {
		falhar(fmt.Errorf("assinatura INVÁLIDA"))
	}
	fmt.Println("assinatura OK")
}

func chavePrivada() ed25519.PrivateKey {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("PONTE_CHAVE_PRIVADA")))
	if err != nil || len(b) != ed25519.SeedSize {
		falhar(fmt.Errorf("PONTE_CHAVE_PRIVADA ausente ou inválida (gere com: ponte-assinar chaves)"))
	}
	return ed25519.NewKeyFromSeed(b)
}

func falhar(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERRO:", err)
		os.Exit(1)
	}
}

func uso() {
	fmt.Println("uso: ponte-assinar chaves | manifesto [...] | verificar [...]")
	os.Exit(2)
}
