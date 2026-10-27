package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"time"

	"ponte/internal/atualizacao"
	"ponte/internal/config"
)

// atualizador confere o manifesto de tempos em tempos (ou na hora, quando chega o comando
// "atualizar" e o worker cria o arquivo atualizar.agora), troca o que mudou e desfaz se a
// versão nova não responder.
type atualizador struct {
	cfg *config.Config
	pub ed25519.PublicKey
	sup *supervisor
	log *log.Logger
}

// estado sobrevive a reinícios: evita reaplicar versão que já falhou e
// recusa manifesto mais velho que o último aplicado.
type estado struct {
	UltimoManifesto time.Time `json:"ultimo_manifesto"`
	Recusados       []string  `json:"recusados"` // sha256 que falharam no teste
}

type saude struct {
	Versao        string `json:"versao"`
	BancoOK       bool   `json:"banco_ok"`
	ConsultasSHA  string `json:"consultas_sha"`
	ConsultasErro string `json:"consultas_erro"`
}

func (a *atualizador) rodar(ctx context.Context) {
	if a.cfg.Atualizacao.ManifestoURL == "" {
		a.log.Printf("atualização automática desligada (sem manifesto_url)")
		return
	}
	intervalo := time.Duration(a.cfg.Atualizacao.IntervaloMinutos) * time.Minute
	proxima := time.Now().Add(30 * time.Second) // dá tempo do worker subir antes da primeira checagem
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		flag := a.cfg.Caminho(config.ArquivoAtualizarAgora)
		if _, err := os.Stat(flag); err == nil {
			os.Remove(flag)
			a.log.Printf("atualização pedida pelo comando \"atualizar\" (Supabase)")
			proxima = time.Now()
		}
		if time.Now().Before(proxima) {
			continue
		}
		a.verificar(ctx)
		proxima = time.Now().Add(intervalo)
	}
}

func (a *atualizador) verificar(ctx context.Context) {
	m, err := atualizacao.BaixarManifesto(ctx, a.cfg.Atualizacao.ManifestoURL, a.pub)
	if err != nil {
		a.log.Printf("manifesto: %v", err)
		return
	}
	if m.Cliente != a.cfg.Cliente {
		a.log.Printf("manifesto é do cliente %q, este é %q: ignorando", m.Cliente, a.cfg.Cliente)
		return
	}
	est := a.lerEstado()
	if m.GeradoEm.Before(est.UltimoManifesto) {
		a.log.Printf("manifesto de %s é mais antigo que o último aplicado (%s): ignorando", m.GeradoEm, est.UltimoManifesto)
		return
	}

	// Worker primeiro: um queries.yaml novo pode depender de recurso do worker novo.
	if m.API.SHA256 != "" && !slices.Contains(est.Recusados, m.API.SHA256) {
		atual, _ := atualizacao.SHA256Arquivo(a.cfg.Caminho("worker-fdc.exe"))
		if atual != m.API.SHA256 {
			if err := a.atualizarWorker(ctx, m); err != nil {
				a.log.Printf("ATUALIZAÇÃO DO WORKER FALHOU: %v", err)
				est.Recusados = append(est.Recusados, m.API.SHA256)
			}
		}
	}
	if m.Consultas.SHA256 != "" && !slices.Contains(est.Recusados, m.Consultas.SHA256) {
		atual, _ := atualizacao.SHA256Arquivo(a.cfg.Caminho("queries.yaml"))
		if atual != m.Consultas.SHA256 {
			if err := a.atualizarConsultas(ctx, m); err != nil {
				a.log.Printf("ATUALIZAÇÃO DO queries.yaml FALHOU: %v", err)
				est.Recusados = append(est.Recusados, m.Consultas.SHA256)
			}
		}
	}
	est.UltimoManifesto = m.GeradoEm
	a.salvarEstado(est)
}

func (a *atualizador) atualizarWorker(ctx context.Context, m *atualizacao.Manifesto) error {
	var (
		exe      = a.cfg.Caminho("worker-fdc.exe")
		novo     = a.cfg.Caminho("worker-fdc.novo.exe")
		anterior = a.cfg.Caminho("worker-fdc.anterior.exe")
	)
	a.log.Printf("baixando worker-fdc %s", m.API.Versao)
	if err := atualizacao.BaixarComponente(ctx, a.cfg.Atualizacao.ManifestoURL, m.API, novo); err != nil {
		return err
	}
	antes, _ := a.saude(ctx)

	a.sup.pausar()
	os.Remove(anterior)
	if err := renomear(exe, anterior); err != nil {
		a.sup.retomar()
		return fmt.Errorf("guardando versão atual: %w", err)
	}
	if err := renomear(novo, exe); err != nil {
		renomear(anterior, exe)
		a.sup.retomar()
		return fmt.Errorf("colocando versão nova: %w", err)
	}
	a.sup.retomar()

	// Banco fora do ar não é culpa da versão nova: só exige banco_ok se antes estava ok.
	ok := a.esperar(ctx, 60*time.Second, func(s saude) bool {
		return (m.API.Versao == "" || s.Versao == m.API.Versao) && (s.BancoOK || antes == nil || !antes.BancoOK)
	})
	if ok {
		a.log.Printf("worker-fdc atualizado para %s", m.API.Versao)
		return nil
	}

	a.log.Printf("versão %s não respondeu bem, voltando a anterior", m.API.Versao)
	a.sup.pausar()
	os.Remove(a.cfg.Caminho("worker-fdc.recusada.exe"))
	renomear(exe, a.cfg.Caminho("worker-fdc.recusada.exe"))
	err := renomear(anterior, exe)
	a.sup.retomar()
	if err != nil {
		return fmt.Errorf("versão nova falhou E não consegui restaurar a anterior: %w", err)
	}
	return fmt.Errorf("versão %s recusada (anterior restaurada)", m.API.Versao)
}

func (a *atualizador) atualizarConsultas(ctx context.Context, m *atualizacao.Manifesto) error {
	var (
		atual    = a.cfg.Caminho("queries.yaml")
		novo     = a.cfg.Caminho("queries.novo.yaml")
		anterior = a.cfg.Caminho("queries.anterior.yaml")
	)
	if err := atualizacao.BaixarComponente(ctx, a.cfg.Atualizacao.ManifestoURL, m.Consultas, novo); err != nil {
		return err
	}
	if b, err := os.ReadFile(atual); err == nil {
		os.WriteFile(anterior, b, 0o644)
	}
	if err := renomear(novo, atual); err != nil {
		return err
	}
	// O worker relê o queries.yaml a cada 5 s; confere se ele aceitou o arquivo novo.
	var ultima saude
	if a.esperar(ctx, 30*time.Second, func(s saude) bool { ultima = s; return s.ConsultasSHA == m.Consultas.SHA256 }) {
		a.log.Printf("queries.yaml atualizado (%s)", m.Consultas.Versao)
		return nil
	}
	if b, err := os.ReadFile(anterior); err == nil {
		os.WriteFile(atual, b, 0o644)
	}
	return fmt.Errorf("worker não aceitou o queries.yaml novo (%s); anterior restaurado", ultima.ConsultasErro)
}

func (a *atualizador) saude(ctx context.Context) (*saude, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.Local.URLLocal()+"/health", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var s saude
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (a *atualizador) esperar(ctx context.Context, limite time.Duration, ok func(saude) bool) bool {
	fim := time.Now().Add(limite)
	for time.Now().Before(fim) && ctx.Err() == nil {
		if s, err := a.saude(ctx); err == nil && ok(*s) {
			return true
		}
		dormir(ctx, 2*time.Second)
	}
	return false
}

func (a *atualizador) lerEstado() estado {
	var e estado
	if b, err := os.ReadFile(a.cfg.Caminho("estado.json")); err == nil {
		json.Unmarshal(b, &e)
	}
	return e
}

func (a *atualizador) salvarEstado(e estado) {
	if len(e.Recusados) > 50 {
		e.Recusados = e.Recusados[len(e.Recusados)-50:]
	}
	b, _ := json.MarshalIndent(e, "", "  ")
	if err := os.WriteFile(a.cfg.Caminho("estado.json"), b, 0o644); err != nil {
		a.log.Printf("salvando estado.json: %v", err)
	}
}

// renomear tenta por alguns segundos: logo depois de matar o processo, o
// Windows ainda pode estar segurando o .exe.
func renomear(de, para string) error {
	var err error
	for i := 0; i < 20; i++ {
		if err = os.Rename(de, para); err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return err
}
