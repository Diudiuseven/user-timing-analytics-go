package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Diudiuseven/user-timing-analytics-go/model"
)

// File is a directory-backed Store: batches append to "journal.jsonl" and the
// state snapshot is written to "state.json" atomically.
type File struct {
	dir       string
	journal   string
	statePath string
}

// OpenFile prepares (or reopens) a directory-backed store.
func OpenFile(dir string) (*File, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &File{dir: dir, journal: filepath.Join(dir, "journal.jsonl"), statePath: filepath.Join(dir, "state.json")}, nil
}

// AppendBatch appends one batch line and flushes before returning.
func (f *File) AppendBatch(b model.Batch) error {
	fh, err := os.OpenFile(f.journal, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer fh.Close()
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if _, err := fh.Write(append(data, '\n')); err != nil {
		return err
	}
	return fh.Sync()
}

// ListBatches reads every journal line in order.
func (f *File) ListBatches() ([]model.Batch, error) {
	fh, err := os.Open(f.journal)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	var out []model.Batch
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var b model.Batch
		if err := json.Unmarshal(line, &b); err != nil {
			return nil, fmt.Errorf("corrupt journal line: %w", err)
		}
		out = append(out, b)
	}
	return out, sc.Err()
}

// SaveState writes the snapshot to a temp file then renames it into place.
func (f *File) SaveState(s *State) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := f.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, f.statePath); err != nil {
		return err
	}
	return nil
}

// LoadState reads the snapshot.
func (f *File) LoadState() (*State, bool, error) {
	data, err := os.ReadFile(f.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, false, err
	}
	return &s, true, nil
}
