package gate

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// auditStore writes evidence records to per-session JSONL files for
// durability beyond process lifetime. Optional — Gate works without
// it (in-memory only) when AuditDir is empty.
//
// Format: one JSON-encoded Evidence record per line, file opened
// O_APPEND so concurrent writes from different scry processes are
// safe at the kernel level (each Write is atomic for buffers ≤ PIPE_BUF).
// We still serialise through a per-session mutex to keep chain hashes
// consistent within one process.
type auditStore struct {
	dir string

	mu     sync.Mutex
	files  map[SessionID]*os.File
	writers map[SessionID]*bufio.Writer
}

func newAuditStore(dir string) (*auditStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("gate: mkdir audit dir: %w", err)
	}
	return &auditStore{
		dir:     dir,
		files:   map[SessionID]*os.File{},
		writers: map[SessionID]*bufio.Writer{},
	}, nil
}

// append writes one record to the session's JSONL file. Flushes the
// bufio writer immediately so a crash doesn't lose audit data —
// audit > throughput.
func (s *auditStore) append(session SessionID, ev Evidence) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.writerLocked(session)
	if err != nil {
		return err
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal evidence: %w", err)
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write evidence: %w", err)
	}
	return w.Flush()
}

// writerLocked opens (or returns the cached) writer for a session.
// Caller MUST hold s.mu.
func (s *auditStore) writerLocked(session SessionID) (*bufio.Writer, error) {
	if w, ok := s.writers[session]; ok {
		return w, nil
	}
	path := s.pathFor(session)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open audit file: %w", err)
	}
	w := bufio.NewWriter(f)
	s.files[session] = f
	s.writers[session] = w
	return w, nil
}

// load replays a session's JSONL file into a slice of Evidence. Used
// at Gate startup so the in-memory chain matches the persisted one.
// Returns nil + nil when the file doesn't exist (clean start).
func (s *auditStore) load(session SessionID) ([]Evidence, error) {
	path := s.pathFor(session)
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open audit file: %w", err)
	}
	defer func() { _ = f.Close() }()

	var out []Evidence
	sc := bufio.NewScanner(f)
	// 1 MiB max line length — Evidence records carry only hashes,
	// not the query/response bodies themselves, so this is plenty.
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev Evidence
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, fmt.Errorf("parse evidence line %d: %w", len(out)+1, err)
		}
		out = append(out, ev)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan audit file: %w", err)
	}
	return out, nil
}

// loadAllSessions returns every session JSONL file in the audit dir
// paired with its Evidence chain. Used at Gate init to repopulate
// the in-memory state from disk so VerifyChain works across
// restarts.
func (s *auditStore) loadAllSessions() (map[SessionID][]Evidence, error) {
	out := map[SessionID][]Evidence{}
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read audit dir: %w", err)
	}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".jsonl") {
			continue
		}
		session := SessionID(strings.TrimSuffix(ent.Name(), ".jsonl"))
		chain, err := s.load(session)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", ent.Name(), err)
		}
		out[session] = chain
	}
	return out, nil
}

// Close flushes + closes every open file. Idempotent; safe to call
// multiple times.
func (s *auditStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var firstErr error
	for k, w := range s.writers {
		if err := w.Flush(); err != nil && firstErr == nil {
			firstErr = err
		}
		if err := s.files[k].Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	s.writers = nil
	s.files = nil
	return firstErr
}

func (s *auditStore) pathFor(session SessionID) string {
	return filepath.Join(s.dir, safeSessionName(string(session))+".jsonl")
}

// safeSessionName escapes a session identifier (which might be an
// arbitrary client name) into a safe filename leaf. Same rule as
// runtime.safeIndexName — keep [a-zA-Z0-9_-], replace everything
// else with '_'.
func safeSessionName(name string) string {
	if name == "" {
		return "_empty"
	}
	out := make([]byte, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_':
			out[i] = c
		default:
			out[i] = '_'
		}
	}
	return string(out)
}
