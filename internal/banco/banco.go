// Package banco executa as consultas no Firebird sempre em transação curta,
// somente leitura, com timeout e limite de consultas simultâneas, para nunca
// pesar no ERP do cliente.
package banco

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"strings"
	"time"
	"unicode/utf8"

	_ "github.com/nakagami/firebirdsql"
	"golang.org/x/text/encoding/charmap"
)

type Banco struct {
	db      *sql.DB
	vagas   chan struct{}
	timeout time.Duration
}

var ErrOcupado = errors.New("banco ocupado: muitas consultas ao mesmo tempo")

func Abrir(dsn string, maxSimultaneas int, timeout time.Duration) (*Banco, error) {
	// Se o servidor não atender o cancelamento de uma consulta que estourou o tempo, derruba o socket.
	if !strings.Contains(dsn, "cancel_hard_drop") {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		dsn += sep + "cancel_hard_drop=true"
	}
	db, err := sql.Open("firebirdsql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(maxSimultaneas)
	db.SetMaxIdleConns(2)
	// Conexões renovadas com frequência: se o ERP/servidor reiniciar, nenhuma fica pendurada.
	db.SetConnMaxLifetime(10 * time.Minute)
	db.SetConnMaxIdleTime(2 * time.Minute)
	return &Banco{db: db, vagas: make(chan struct{}, maxSimultaneas), timeout: timeout}, nil
}

func (b *Banco) Fechar() error { return b.db.Close() }

func (b *Banco) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return b.db.PingContext(ctx)
}

type Resultado struct {
	Colunas  []string         `json:"colunas"`
	Linhas   []map[string]any `json:"linhas"`
	Truncado bool             `json:"truncado"`
}

// ForaDoAr diz se o erro é de conexão (Firebird parado, rede, socket derrubado),
// e não de SQL.
func ForaDoAr(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) || errors.Is(err, driver.ErrBadConn) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func (b *Banco) Consultar(ctx context.Context, query string, args []any, maxLinhas int) (*Resultado, error) {
	var res *Resultado
	err := b.executar(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		res, err = consultar(ctx, tx, query, args, maxLinhas)
		return err
	})
	return res, err
}

// Binario devolve a primeira coluna da primeira linha como veio do banco, sem
// conversão de texto (BLOB de imagem). Sem linha ou coluna nula: nil, nil.
func (b *Banco) Binario(ctx context.Context, query string, args []any) ([]byte, error) {
	var out []byte
	err := b.executar(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var v any
		err := tx.QueryRowContext(ctx, query, args...).Scan(&v)
		switch x := v.(type) {
		case []byte:
			out = x
		case string:
			out = []byte(x)
		}
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	})
	return out, err
}

// executar roda fn numa transação curta, somente leitura, com timeout e vaga no limite
// de consultas simultâneas.
func (b *Banco) executar(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()

	select {
	case b.vagas <- struct{}{}:
		defer func() { <-b.vagas }()
	case <-ctx.Done():
		return ErrOcupado
	}

	// Se o Firebird reiniciou, as conexões paradas no pool morreram e só se
	// descobre isso ao usar. Como é só leitura, repetir com outra conexão é seguro.
	var err error
	for tentativa := 0; tentativa < 3; tentativa++ {
		err = b.emTransacao(ctx, fn)
		if err == nil || !ForaDoAr(err) || ctx.Err() != nil {
			break
		}
	}
	return err
}

func (b *Banco) emTransacao(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	tx, err := b.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("abrindo transação: %w", err)
	}
	// Somente leitura: rollback e commit têm o mesmo efeito; rollback é o mais seguro.
	defer tx.Rollback()
	return fn(ctx, tx)
}

func consultar(ctx context.Context, tx *sql.Tx, query string, args []any, maxLinhas int) (*Resultado, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	for i := range cols {
		cols[i] = strings.ToLower(cols[i])
	}
	res := &Resultado{Colunas: cols, Linhas: []map[string]any{}}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if len(res.Linhas) == maxLinhas {
			res.Truncado = true
			break
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		linha := make(map[string]any, len(cols))
		for i, c := range cols {
			linha[c] = limpar(vals[i])
		}
		res.Linhas = append(res.Linhas, linha)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return res, nil
}

// limpar deixa o valor pronto para JSON: tira o preenchimento de CHAR,
// formata datas e corta o ruído de DOUBLE (1299.8999999 -> 1299.9).
func limpar(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		return strings.TrimRight(x, " ")
	case []byte: // BLOB de texto vem cru, sem conversão de charset
		if !utf8.Valid(x) {
			if d, err := charmap.Windows1252.NewDecoder().Bytes(x); err == nil {
				x = d
			}
		}
		return strings.TrimRight(string(x), " ")
	case time.Time:
		if x.Hour() == 0 && x.Minute() == 0 && x.Second() == 0 {
			return x.Format("2006-01-02")
		}
		return x.Format("2006-01-02 15:04:05")
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil
		}
		return math.Round(x*10000) / 10000
	case float32:
		return limpar(float64(x))
	}
	if s, ok := v.(fmt.Stringer); ok { // NUMERIC/DECIMAL vêm como tipo decimal do driver
		return s.String()
	}
	return v
}
