package agent

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// sensitiveJobLogPath keeps unredacted output on /run tmpfs only.
func sensitiveJobLogPath(jobID string) string {
	return filepath.Join(agentRuntimeDir("job-logs"), jobID+".log")
}

func agentRuntimeDir(name string) string {
	return filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "pluto", name)
}

// redactedJobLog buffers a short tail so credentials split across output
// chunks are redacted before any bytes reach durable logs or the client.
type redactedJobLog struct {
	file     *os.File
	secrets  []string
	pending  []byte
	emit     func([]byte)
	writeErr error
}

func (s *redactedJobLog) write(chunk []byte) {
	s.pending = append(s.pending, chunk...)
	max := 0
	for _, secret := range s.secrets {
		if len(secret) > max {
			max = len(secret)
		}
	}
	cut := len(s.pending) - max + 1
	if cut < 0 {
		cut = 0
	}
	for changed := true; changed; {
		changed = false
		for _, secret := range s.secrets {
			if len(secret) == 0 {
				continue
			}
			if i := bytes.Index(s.pending, []byte(secret)); i >= 0 && i < cut && i+len(secret) > cut {
				cut = i + len(secret)
				changed = true
			}
		}
	}
	if cut == 0 {
		return
	}
	s.flushPrefix(cut)
}

func (s *redactedJobLog) flushPrefix(count int) {
	if count > len(s.pending) {
		count = len(s.pending)
	}
	data := string(s.pending[:count])
	s.pending = append(s.pending[:0], s.pending[count:]...)
	for _, secret := range s.secrets {
		if secret != "" {
			data = strings.ReplaceAll(data, secret, "[REDACTED]")
		}
	}
	if data == "" {
		return
	}
	if _, err := s.file.WriteString(data); err != nil && s.writeErr == nil {
		s.writeErr = err
	}
	if s.emit != nil {
		s.emit([]byte(data))
	}
}

func (s *redactedJobLog) finish() error {
	s.flushPrefix(len(s.pending))
	return errors.Join(s.writeErr, s.file.Close())
}
