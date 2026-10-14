//go:build linux

package terminal

import (
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// Open allocates a kernel terminal pair the daemon owns. The master is the
// daemon's side; the slave is handed into the box as a process's stdio.
func Open() (PTY, error) {
	master, err := os.OpenFile("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, fmt.Errorf("open pty master: %w", err)
	}
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		master.Close()
		return nil, fmt.Errorf("unlock pty: %w", err)
	}
	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		master.Close()
		return nil, fmt.Errorf("resolve pty number: %w", err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, fmt.Errorf("open pty slave: %w", err)
	}
	return &linuxPTY{master: master, slave: slave}, nil
}

type linuxPTY struct {
	master, slave *os.File
}

func (p *linuxPTY) Master() io.ReadWriteCloser { return p.master }
func (p *linuxPTY) Slave() io.ReadWriteCloser  { return p.slave }
func (p *linuxPTY) Close() error {
	return errors.Join(p.master.Close(), p.slave.Close())
}
