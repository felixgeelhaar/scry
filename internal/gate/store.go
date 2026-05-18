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
//
// Rotation: when maxSize > 0 and the current file exceeds it after a
// write, the store shifts archives — file.jsonl.<keep-1> → .<keep>,
// ..., file.jsonl.1 → .2, file.jsonl → .1, then opens a fresh file.
// Chain continuity survives because the in-memory chain already
// carries the previous ChainHash; the new file's first record links
// to it via that hash, exactly as if rotation hadn't happened.
type auditStore struct {
	dir     string
	maxSize int64 // bytes; 0 = unlimited
	keep    int   // archives; 0 = unlimited

	mu      sync.Mutex
	files   map[SessionID]*os.File
	writers map[SessionID]*bufio.Writer
	sizes   map[SessionID]int64 // bytes written to current file
}

func newAuditStore(dir string, maxSize int64, keep int) (*auditStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("gate: mkdir audit dir: %w", err)
	}
	return &auditStore{
		dir:     dir,
		maxSize: maxSize,
		keep:    keep,
		files:   map[SessionID]*os.File{},
		writers: map[SessionID]*bufio.Writer{},
		sizes:   map[SessionID]int64{},
	}, nil
}

// append writes one record to the session's JSONL file. Flushes the
// bufio writer immediately so a crash doesn't lose audit data —
// audit > throughput. Rotates the file when maxSize is configured
// and the current size exceeds it.
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
	b = append(b, '\n')
	n, err := w.Write(b)
	if err != nil {
		return fmt.Errorf("write evidence: %w", err)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	s.sizes[session] += int64(n)

	// Post-write rotation: rolling on the LAST record that pushed
	// past the threshold keeps the linked chain intact (the
	// just-written record is the new tail of the archive that
	// goes to .1, the next append starts a new file linking via
	// in-memory ChainHash).
	if s.maxSize > 0 && s.sizes[session] >= s.maxSize {
		if err := s.rotateLocked(session); err != nil {
			return fmt.Errorf("rotate: %w", err)
		}
	}
	return nil
}

// rotateLocked closes the current file, shifts archives by one slot
// (dropping the oldest when keep is set), and reopens a fresh
// <session>.jsonl. Caller MUST hold s.mu.
func (s *auditStore) rotateLocked(session SessionID) error {
	// Flush + close current.
	if w, ok := s.writers[session]; ok {
		if err := w.Flush(); err != nil {
			return err
		}
		if f, fok := s.files[session]; fok {
			if err := f.Close(); err != nil {
				return err
			}
		}
		delete(s.writers, session)
		delete(s.files, session)
	}

	base := s.pathFor(session)

	// Drop the oldest archive when keep would be exceeded. With
	// keep=0 (unlimited), retain everything indefinitely.
	if s.keep > 0 {
		oldest := fmt.Sprintf("%s.%d", base, s.keep)
		_ = os.Remove(oldest) // ignore missing
	}

	// Shift: walk from high to low so we don't clobber.
	highest := s.keep
	if highest == 0 {
		// Find current top of stack by stat probing.
		highest = s.highestArchiveLocked(base)
	}
	for i := highest; i >= 1; i-- {
		from := fmt.Sprintf("%s.%d", base, i)
		to := fmt.Sprintf("%s.%d", base, i+1)
		if _, err := os.Stat(from); err == nil {
			if err := os.Rename(from, to); err != nil {
				return fmt.Errorf("shift %s → %s: %w", from, to, err)
			}
		}
	}

	// Current → .1
	if _, err := os.Stat(base); err == nil {
		if err := os.Rename(base, base+".1"); err != nil {
			return fmt.Errorf("rename %s → %s.1: %w", base, base, err)
		}
	}
	// Reset size counter; next append re-opens via writerLocked.
	s.sizes[session] = 0
	return nil
}

// highestArchiveLocked probes for the largest existing .N suffix so
// unbounded-keep rotations know where the chain ends. Caller MUST
// hold s.mu.
func (s *auditStore) highestArchiveLocked(base string) int {
	for i := 1; ; i++ {
		if _, err := os.Stat(fmt.Sprintf("%s.%d", base, i)); err != nil {
			return i - 1
		}
	}
}

// writerLocked opens (or returns the cached) writer for a session.
// Stats the existing file so the rolling size counter survives
// process restarts (otherwise a long-lived session would rotate at
// `maxSize` worth of *fresh* writes regardless of prior file size).
// Caller MUST hold s.mu.
func (s *auditStore) writerLocked(session SessionID) (*bufio.Writer, error) {
	if w, ok := s.writers[session]; ok {
		return w, nil
	}
	path := s.pathFor(session)
	if info, err := os.Stat(path); err == nil {
		s.sizes[session] = info.Size()
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open audit file: %w", err)
	}
	w := bufio.NewWriter(f)
	s.files[session] = f
	s.writers[session] = w
	return w, nil
}

// load replays a session's JSONL files into a single Evidence slice
// in chronological order. Walks archives oldest-to-newest
// (.jsonl.N, .jsonl.N-1, ..., .jsonl.1) then the current .jsonl so
// the in-memory chain matches what VerifyChain would compute across
// the full history. Returns nil + nil when nothing exists for this
// session.
func (s *auditStore) load(session SessionID) ([]Evidence, error) {
	base := s.pathFor(session)
	var paths []string
	// Find the highest archive number we have for this session.
	for i := 1; ; i++ {
		p := fmt.Sprintf("%s.%d", base, i)
		if _, err := os.Stat(p); err != nil {
			break
		}
		paths = append(paths, p)
	}
	// Reverse: chronological order = oldest first = highest .N
	// down to .1.
	for i, j := 0, len(paths)-1; i < j; i, j = i+1, j-1 {
		paths[i], paths[j] = paths[j], paths[i]
	}
	// Current file last.
	if _, err := os.Stat(base); err == nil {
		paths = append(paths, base)
	}
	if len(paths) == 0 {
		return nil, nil
	}

	var out []Evidence
	for _, p := range paths {
		recs, err := readEvidenceFile(p)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
	return out, nil
}

// readEvidenceFile parses one JSONL audit file into Evidence
// records. Reused by load() across the full archive chain.
func readEvidenceFile(path string) ([]Evidence, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open audit file %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var out []Evidence
	sc := bufio.NewScanner(f)
	// 1 MiB max line length — Evidence records carry only hashes,
	// not the query/response bodies themselves.
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev Evidence
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, fmt.Errorf("parse evidence in %s line %d: %w", path, len(out)+1, err)
		}
		out = append(out, ev)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan audit file %s: %w", path, err)
	}
	return out, nil
}

// loadAllSessions returns every session JSONL chain in the audit
// dir paired with its replayed Evidence slice. Used at Gate init to
// repopulate the in-memory state from disk so VerifyChain works
// across restarts AND across rotation boundaries.
//
// Recognises both current files (<safe-session>.jsonl) and rotated
// archives (<safe-session>.jsonl.N). The session set is the union
// of safe-session-name stems extracted from each.
func (s *auditStore) loadAllSessions() (map[SessionID][]Evidence, error) {
	out := map[SessionID][]Evidence{}
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read audit dir: %w", err)
	}
	sessions := map[SessionID]bool{}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		// Current file: <session>.jsonl
		if strings.HasSuffix(name, ".jsonl") {
			sessions[SessionID(strings.TrimSuffix(name, ".jsonl"))] = true
			continue
		}
		// Archive: <session>.jsonl.<N> — strip the suffix tail.
		if idx := strings.LastIndex(name, ".jsonl."); idx >= 0 {
			sessions[SessionID(name[:idx])] = true
		}
	}
	for session := range sessions {
		chain, err := s.load(session)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", session, err)
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
