// worker-fdc-agente: serviço do Windows que mantém o worker-fdc.exe no ar e o atualiza sozinho.
//
//	worker-fdc-agente.exe instalar      registra o serviço "WorkerFDC" (início automático, reinicia se falhar)
//	worker-fdc-agente.exe iniciar | parar | reiniciar | status | desinstalar
//	worker-fdc-agente.exe               roda em primeiro plano (para testar; Ctrl+C encerra)
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/kardianos/service"
	"gopkg.in/natefinch/lumberjack.v2"

	"ponte/internal/atualizacao"
	"ponte/internal/config"
	"ponte/internal/versao"
)

type programa struct {
	dir    string
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func (p *programa) Start(s service.Service) error {
	cfgPath := filepath.Join(p.dir, "config.json")
	cfg, err := config.Carregar(cfgPath)
	if err != nil {
		return err
	}
	saida := io.Writer(&lumberjack.Logger{Filename: cfg.Caminho(filepath.Join("logs", "agente.log")), MaxSize: 10, MaxBackups: 5, MaxAge: 30})
	if service.Interactive() {
		saida = io.MultiWriter(os.Stdout, saida)
	}
	l := log.New(saida, "", log.LstdFlags)
	l.Printf("worker-fdc-agente %s iniciando em %s", versao.Versao, p.dir)

	j, err := novoJob()
	if err != nil {
		l.Printf("aviso: job object indisponível: %v", err)
	}
	sup := &supervisor{
		exe:     cfg.Caminho("worker-fdc.exe"),
		args:    []string{"-config", cfgPath},
		dir:     p.dir,
		log:     l,
		job:     j,
		acordar: make(chan struct{}, 1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	p.wg.Add(1)
	go func() { defer p.wg.Done(); sup.rodar(ctx) }()

	if cfg.Atualizacao.ManifestoURL != "" {
		pub, err := atualizacao.ChavePublica(cfg.Atualizacao.ChavePublica)
		if err != nil {
			l.Printf("atualização automática DESLIGADA: %v", err)
		} else {
			at := &atualizador{cfg: cfg, pub: pub, sup: sup, log: l}
			p.wg.Add(1)
			go func() { defer p.wg.Done(); at.rodar(ctx) }()
		}
	}
	return nil
}

func (p *programa) Stop(s service.Service) error {
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()
	return nil
}

func main() {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	prg := &programa{dir: filepath.Dir(exe)}
	s, err := service.New(prg, &service.Config{
		Name:        "WorkerFDC",
		DisplayName: "Worker FDC (Scale IA)",
		Description: "Mantém o worker que copia o ERP para o Supabase no ar e atualizado. Logs em " + filepath.Join(prg.dir, "logs"),
		Option: service.KeyValue{
			"StartType":              "automatic",
			"DelayedAutoStart":       true, // sobe depois do Firebird no boot
			"OnFailure":              "restart",
			"OnFailureDelayDuration": "10s",
			"OnFailureResetPeriod":   600,
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if len(os.Args) > 1 {
		acoes := map[string]string{
			"instalar": "install", "desinstalar": "uninstall",
			"iniciar": "start", "parar": "stop", "reiniciar": "restart",
		}
		cmd := os.Args[1]
		if cmd == "status" {
			st, err := s.Status()
			nomes := map[service.Status]string{service.StatusRunning: "rodando", service.StatusStopped: "parado"}
			fmt.Println("WorkerFDC:", nomes[st], err)
			return
		}
		acao, ok := acoes[cmd]
		if !ok {
			fmt.Println("uso: worker-fdc-agente.exe [instalar|desinstalar|iniciar|parar|reiniciar|status]")
			os.Exit(2)
		}
		if err := service.Control(s, acao); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", cmd, err)
			os.Exit(1)
		}
		fmt.Println(cmd, "OK")
		return
	}

	if err := s.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
