// Package atualizacao baixa e confere o manifesto publicado pelo CI.
//
// O manifesto (manifest.json) diz qual ponte-api.exe e qual queries.yaml o cliente deve
// ter, com o SHA-256 de cada um. Ele é assinado com ed25519 (manifest.json.sig); a chave
// privada fica só no GitHub Actions e a pública no config.json do cliente. Sem
// assinatura válida, nada é instalado.
package atualizacao

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Manifesto struct {
	Cliente   string     `json:"cliente"`
	GeradoEm  time.Time  `json:"gerado_em"`
	API       Componente `json:"api"`
	Consultas Componente `json:"consultas"`
}

type Componente struct {
	Versao string `json:"versao"`
	URL    string `json:"url"` // relativa ao manifesto ou absoluta
	SHA256 string `json:"sha256"`
}

var cliente = &http.Client{Timeout: 5 * time.Minute}

const tamanhoMaximo = 200 << 20

func ChavePublica(b64 string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("chave_publica inválida (esperado ed25519 em base64)")
	}
	return ed25519.PublicKey(b), nil
}

func Assinar(priv ed25519.PrivateKey, dados []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, dados))
}

func Verificar(pub ed25519.PublicKey, dados []byte, assinaturaB64 string) bool {
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(assinaturaB64))
	return err == nil && ed25519.Verify(pub, dados, sig)
}

// BaixarManifesto busca manifest.json e manifest.json.sig e só devolve se a assinatura bater.
func BaixarManifesto(ctx context.Context, endereco string, pub ed25519.PublicKey) (*Manifesto, error) {
	corpo, err := baixar(ctx, endereco)
	if err != nil {
		return nil, err
	}
	sig, err := baixar(ctx, endereco+".sig")
	if err != nil {
		return nil, err
	}
	if !Verificar(pub, corpo, string(sig)) {
		return nil, errors.New("ASSINATURA INVÁLIDA no manifesto, ignorando")
	}
	var m Manifesto
	if err := json.Unmarshal(corpo, &m); err != nil {
		return nil, fmt.Errorf("manifesto inválido: %w", err)
	}
	return &m, nil
}

// BaixarComponente salva o arquivo em destino somente se o SHA-256 bater com o do manifesto.
func BaixarComponente(ctx context.Context, enderecoManifesto string, c Componente, destino string) error {
	base, err := url.Parse(enderecoManifesto)
	if err != nil {
		return err
	}
	ref, err := url.Parse(c.URL)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.ResolveReference(ref).String(), nil)
	if err != nil {
		return err
	}
	resp, err := cliente.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", c.URL, resp.StatusCode)
	}

	tmp := destino + ".baixando"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, tamanhoMaximo))
	if errFechar := f.Close(); err == nil {
		err = errFechar
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, c.SHA256) {
		os.Remove(tmp)
		return fmt.Errorf("SHA-256 não confere para %s (esperado %s, veio %s)", c.URL, c.SHA256, got)
	}
	return os.Rename(tmp, destino)
}

func SHA256Arquivo(caminho string) (string, error) {
	f, err := os.Open(caminho)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func baixar(ctx context.Context, endereco string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endereco, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := cliente.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", endereco, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}
