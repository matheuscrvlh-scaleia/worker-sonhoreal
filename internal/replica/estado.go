package replica

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// estado guarda, por tabela, o hash de cada linha que o Supabase já confirmou. Assim cada
// ciclo manda só a diferença. Se o arquivo sumir (ou passar de reconciliar_horas), o worker
// pede os hashes ao próprio Supabase, que é quem manda.
type estado struct {
	Reconciliado time.Time         `json:"reconciliado"`
	Hashes       map[string]string `json:"hashes"`
}

func lerEstado(dir, tabela string) (*estado, bool) {
	b, err := os.ReadFile(filepath.Join(dir, tabela+".json"))
	if err != nil {
		return nil, false
	}
	var e estado
	if json.Unmarshal(b, &e) != nil || e.Hashes == nil {
		return nil, false
	}
	return &e, true
}

// salvar grava num temporário e renomeia: queda de energia no meio não deixa arquivo pela metade.
func (e *estado) salvar(dir, tabela string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	destino := filepath.Join(dir, tabela+".json")
	tmp := destino + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, destino)
}
