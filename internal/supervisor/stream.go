package supervisor

import (
	"io"
	"os"
	"os/exec"

	pty "github.com/aymanbagabas/go-pty"
)

// stream is a running child process whose combined output we read and whose
// input we write. Two implementations exist: a PTY-backed one (default, so
// wrapped TUIs render and can be resumed by typing) and a pipe-backed one
// (--no-pty, for CI / non-interactive use and deterministic tests).
type stream interface {
	io.ReadWriteCloser
	Resize(w, h int) error
	Wait() error
	Kill() error
	ExitCode() int
}

// ptyStream runs the child attached to a cross-platform pseudo-terminal.
type ptyStream struct {
	pt  pty.Pty
	cmd *pty.Cmd
}

func newPTYStream(argv []string, env []string, dir string) (*ptyStream, error) {
	pt, err := pty.New()
	if err != nil {
		return nil, err
	}
	c := pt.Command(argv[0], argv[1:]...)
	c.Env = env
	c.Dir = dir
	if err := c.Start(); err != nil {
		pt.Close()
		return nil, err
	}
	return &ptyStream{pt: pt, cmd: c}, nil
}

func (s *ptyStream) Read(p []byte) (int, error)  { return s.pt.Read(p) }
func (s *ptyStream) Write(p []byte) (int, error) { return s.pt.Write(p) }
func (s *ptyStream) Close() error                { return s.pt.Close() }
func (s *ptyStream) Resize(w, h int) error       { return s.pt.Resize(w, h) }
func (s *ptyStream) Wait() error                 { return s.cmd.Wait() }

func (s *ptyStream) Kill() error {
	if s.cmd.Process != nil {
		return s.cmd.Process.Kill()
	}
	return nil
}

func (s *ptyStream) ExitCode() int {
	if s.cmd.ProcessState != nil {
		return s.cmd.ProcessState.ExitCode()
	}
	return 0
}

// pipeStream runs the child with ordinary pipes, merging stdout+stderr into one
// reader. No terminal semantics, but fully portable and deterministic.
type pipeStream struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out io.ReadCloser
}

func newPipeStream(argv []string, env []string, dir string) (*pipeStream, error) {
	c := exec.Command(argv[0], argv[1:]...)
	c.Env = env
	c.Dir = dir
	in, err := c.StdinPipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	c.Stdout = outW
	c.Stderr = outW
	if err := c.Start(); err != nil {
		outR.Close()
		outW.Close()
		return nil, err
	}
	outW.Close() // child holds the write end; we read until all writers close
	return &pipeStream{cmd: c, in: in, out: outR}, nil
}

func (s *pipeStream) Read(p []byte) (int, error)  { return s.out.Read(p) }
func (s *pipeStream) Write(p []byte) (int, error) { return s.in.Write(p) }

func (s *pipeStream) Close() error {
	s.in.Close()
	return s.out.Close()
}

func (s *pipeStream) Resize(int, int) error { return nil }
func (s *pipeStream) Wait() error           { return s.cmd.Wait() }

func (s *pipeStream) Kill() error {
	if s.cmd.Process != nil {
		return s.cmd.Process.Kill()
	}
	return nil
}

func (s *pipeStream) ExitCode() int {
	if s.cmd.ProcessState != nil {
		return s.cmd.ProcessState.ExitCode()
	}
	return 0
}
