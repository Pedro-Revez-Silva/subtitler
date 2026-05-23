package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type Store struct {
	path string
	data Data
}

type Data struct {
	Files map[string]FileState `json:"files"`
}

type FileState struct {
	Path        string               `json:"path"`
	Source      string               `json:"source"`
	UpdatedAt   time.Time            `json:"updated_at"`
	Languages   map[string]LangState `json:"languages"`
	LastError   string               `json:"last_error,omitempty"`
	Attempts    int                  `json:"attempts"`
	FileSize    int64                `json:"file_size"`
	ModTimeUnix int64                `json:"mod_time_unix"`
}

type LangState struct {
	OutputPath string    `json:"output_path"`
	Generated  bool      `json:"generated"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func Open(path string) (*Store, error) {
	store := &Store{
		path: path,
		data: Data{Files: map[string]FileState{}},
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return store, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, &store.data); err != nil {
		return nil, err
	}
	if store.data.Files == nil {
		store.data.Files = map[string]FileState{}
	}
	return store, nil
}

func (s *Store) Get(path string) (FileState, bool) {
	state, ok := s.data.Files[path]
	return state, ok
}

func (s *Store) Put(path string, value FileState) {
	if value.Languages == nil {
		value.Languages = map[string]LangState{}
	}
	value.Path = path
	value.UpdatedAt = time.Now()
	s.data.Files[path] = value
}

func (s *Store) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil && filepath.Dir(s.path) != "." {
		return err
	}
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}
