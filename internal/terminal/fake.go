package terminal

import (
	"errors"
	"io"
)

// PipeOpener allocates an in-memory terminal pair. Tests use it to drive the
// same attach/watch verbs as a real PTY without a kernel tty, so a fake box's
// exec can script output and read keystrokes deterministically.
func PipeOpener() (PTY, error) { return NewPipe(), nil }

// NewPipe returns an in-memory PTY: writing the master reaches the slave and
// vice versa. Closing it unblocks a pending read on either end.
func NewPipe() PTY {
	inR, inW := io.Pipe()   // master -> slave (keyboard input)
	outR, outW := io.Pipe() // slave -> master (program output)
	return &pipePTY{
		master: &pipeEnd{r: outR, w: inW},
		slave:  &pipeEnd{r: inR, w: outW},
	}
}

type pipePTY struct {
	master, slave *pipeEnd
}

func (p *pipePTY) Master() io.ReadWriteCloser { return p.master }
func (p *pipePTY) Slave() io.ReadWriteCloser  { return p.slave }
func (p *pipePTY) Close() error {
	return errors.Join(p.master.Close(), p.slave.Close())
}

// pipeEnd joins one read end and one write end into a single stream.
type pipeEnd struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (e *pipeEnd) Read(p []byte) (int, error)  { return e.r.Read(p) }
func (e *pipeEnd) Write(p []byte) (int, error) { return e.w.Write(p) }
func (e *pipeEnd) Close() error                { return errors.Join(e.r.Close(), e.w.Close()) }
