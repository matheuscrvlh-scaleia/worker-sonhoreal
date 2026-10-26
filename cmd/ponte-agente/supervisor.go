package main

import (
	"context"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// supervisor mantém o ponte-api.exe rodando: se ele cair, sobe de novo
// (esperando 1s, 2s, 4s... até 30s se ficar caindo em sequência).
type supervisor struct {
	exe     string
	args    []string
	dir     string
	log     *log.Logger
	job     job
	mu      sync.Mutex
	cmd     *exec.Cmd
	saiu    chan struct{}
	pausado bool
	acordar chan struct{} // buffer 1
}

func (s *supervisor) rodar(ctx context.Context) {
	espera := time.Second
	for ctx.Err() == nil {
		if s.estaPausado() {
			dormir(ctx, 300*time.Millisecond)
			continue
		}
		inicio := time.Now()
		saiu, err := s.iniciar()
		if err != nil {
			s.log.Printf("não consegui iniciar %s: %v", s.exe, err)
		} else {
			select {
			case <-ctx.Done():
				s.matar()
				return
			case <-saiu:
			}
			if s.estaPausado() {
				continue // parada pedida pelo atualizador
			}
		}
		if time.Since(inicio) > time.Minute {
			espera = time.Second
		}
		s.log.Printf("ponte-api fora do ar, tentando de novo em %s", espera)
		select {
		case <-ctx.Done():
		case <-time.After(espera):
			espera = min(espera*2, 30*time.Second)
		case <-s.acordar: // atualizador trocou o .exe: sobe já, sem herdar o backoff
			espera = time.Second
		}
	}
	s.matar()
}

func (s *supervisor) iniciar() (chan struct{}, error) {
	cmd := exec.Command(s.exe, s.args...)
	cmd.Dir = s.dir
	cmd.Stdout = io.Discard // a API já grava o próprio log
	cmd.Stderr = linhasNoLog{s.log, "[api] "}
	configurarProcesso(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if err := s.job.anexar(cmd.Process); err != nil {
		s.log.Printf("aviso: não anexei a API ao job object: %v", err)
	}
	saiu := make(chan struct{})
	s.mu.Lock()
	s.cmd, s.saiu = cmd, saiu
	s.mu.Unlock()
	s.log.Printf("ponte-api iniciada (pid %d)", cmd.Process.Pid)
	go func() {
		err := cmd.Wait()
		s.log.Printf("ponte-api encerrou: %v", err)
		close(saiu)
	}()
	return saiu, nil
}

// pausar derruba a API e não deixa subir de novo até retomar (para trocar o .exe).
func (s *supervisor) pausar() {
	s.mu.Lock()
	s.pausado = true
	s.mu.Unlock()
	s.matar()
}

func (s *supervisor) retomar() {
	s.mu.Lock()
	s.pausado = false
	s.mu.Unlock()
	select {
	case s.acordar <- struct{}{}:
	default:
	}
}

func (s *supervisor) estaPausado() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pausado
}

func (s *supervisor) matar() {
	s.mu.Lock()
	cmd, saiu := s.cmd, s.saiu
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	cmd.Process.Kill()
	select {
	case <-saiu:
	case <-time.After(15 * time.Second):
		s.log.Printf("ponte-api não encerrou em 15s")
	}
}

func dormir(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// linhasNoLog joga a saída de erro da API (panics, por exemplo) no log do agente.
type linhasNoLog struct {
	l      *log.Logger
	prefix string
}

func (w linhasNoLog) Write(p []byte) (int, error) {
	for _, linha := range strings.Split(strings.TrimRight(string(p), "\r\n"), "\n") {
		w.l.Print(w.prefix + strings.TrimRight(linha, "\r"))
	}
	return len(p), nil
}
