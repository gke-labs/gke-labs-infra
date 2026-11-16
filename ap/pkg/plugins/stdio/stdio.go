// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package stdio

import (
	"errors"
	"io"
	"net"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// ErrDeadlinesNotSupported is returned when a deadline method is invoked on a stdio connection.
var ErrDeadlinesNotSupported = errors.New("stdio: deadlines not supported")

type stdioAddr string

func (a stdioAddr) Network() string { return string(a) }
func (a stdioAddr) String() string  { return string(a) }

type baseConn struct{}

func (baseConn) LocalAddr() net.Addr              { return stdioAddr("stdio") }
func (baseConn) RemoteAddr() net.Addr             { return stdioAddr("stdio") }
func (baseConn) SetDeadline(time.Time) error      { return ErrDeadlinesNotSupported }
func (baseConn) SetReadDeadline(time.Time) error  { return ErrDeadlinesNotSupported }
func (baseConn) SetWriteDeadline(time.Time) error { return ErrDeadlinesNotSupported }

// Conn wraps an io.Reader and io.Writer into a net.Conn.
type Conn struct {
	baseConn
	r         io.Reader
	w         io.Writer
	onClose   func()
	closeOnce sync.Once
	closed    atomic.Bool
	closeErr  error
}

// NewConn returns a net.Conn over an io.Reader and io.Writer.
func NewConn(r io.Reader, w io.Writer) *Conn {
	return &Conn{r: r, w: w}
}

func (c *Conn) Read(p []byte) (int, error) {
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	return c.r.Read(p)
}

func (c *Conn) Write(p []byte) (int, error) {
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	return c.w.Write(p)
}

func (c *Conn) Close() error {
	c.closed.Store(true)
	c.closeOnce.Do(func() {
		var errs []error
		if c.onClose != nil {
			c.onClose()
		}
		if rc, ok := c.r.(io.Closer); ok {
			errs = append(errs, rc.Close())
		}
		if wc, ok := c.w.(io.Closer); ok && any(c.w) != any(c.r) {
			errs = append(errs, wc.Close())
		}
		c.closeErr = errors.Join(errs...)
	})
	return c.closeErr
}

var _ net.Conn = (*Conn)(nil)

// Listener implements net.Listener for the plugin side, yielding one
// connection over stdin and stdout and then blocking until closed.
type Listener struct {
	conn      *Conn
	yield     chan net.Conn
	done      chan struct{}
	closeOnce sync.Once
}

// NewListener returns a net.Listener that yields a single net.Conn over in and out.
func NewListener(in io.Reader, out io.Writer) *Listener {
	l := &Listener{
		yield: make(chan net.Conn, 1),
		done:  make(chan struct{}),
	}
	l.conn = &Conn{
		r: in,
		w: out,
		onClose: func() {
			l.closeOnce.Do(func() {
				close(l.done)
			})
		},
	}
	l.yield <- l.conn
	return l
}

func (l *Listener) Accept() (net.Conn, error) {
	select {
	case <-l.done:
		return nil, net.ErrClosed
	default:
	}
	select {
	case <-l.done:
		return nil, net.ErrClosed
	case c := <-l.yield:
		return c, nil
	}
}

func (l *Listener) Close() error {
	var err error
	l.closeOnce.Do(func() {
		close(l.done)
		select {
		case c := <-l.yield:
			err = c.Close()
		default:
		}
	})
	return err
}

func (l *Listener) Addr() net.Addr {
	return stdioAddr("stdio")
}

var _ net.Listener = (*Listener)(nil)

// CommandConn implements net.Conn for the host (ap) side over a started command's
// stdin and stdout pipes. Closing it closes the pipes and waits for the command;
// a command that exits early turns pending reads and writes into errors that carry
// the exit status.
type CommandConn struct {
	baseConn
	cmd       *exec.Cmd
	in        io.WriteCloser
	out       io.ReadCloser
	closeOnce sync.Once
	closeErr  error
	waitOnce  sync.Once
	waitErr   error
	closed    atomic.Bool
}

// NewCommandConn returns a net.Conn over a started command's stdin and stdout pipes.
func NewCommandConn(cmd *exec.Cmd, in io.WriteCloser, out io.ReadCloser) *CommandConn {
	return &CommandConn{
		cmd: cmd,
		in:  in,
		out: out,
	}
}

func (c *CommandConn) wait() error {
	c.waitOnce.Do(func() {
		c.waitErr = c.cmd.Wait()
	})
	return c.waitErr
}

func (c *CommandConn) Read(p []byte) (int, error) {
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	n, err := c.out.Read(p)
	if err != nil {
		if c.closed.Load() {
			return n, net.ErrClosed
		}
		if n > 0 && err == io.EOF {
			return n, nil
		}
		if werr := c.wait(); werr != nil {
			return n, werr
		}
		return n, err
	}
	return n, nil
}

func (c *CommandConn) Write(p []byte) (int, error) {
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	n, err := c.in.Write(p)
	if err != nil {
		if c.closed.Load() {
			return n, net.ErrClosed
		}
		if werr := c.wait(); werr != nil {
			return n, werr
		}
		return n, err
	}
	return n, nil
}

func (c *CommandConn) Close() error {
	c.closed.Store(true)
	c.closeOnce.Do(func() {
		var errs []error
		if c.in != nil {
			errs = append(errs, c.in.Close())
		}
		if c.out != nil {
			errs = append(errs, c.out.Close())
		}
		errs = append(errs, c.wait())
		c.closeErr = errors.Join(errs...)
	})
	return c.closeErr
}

var _ net.Conn = (*CommandConn)(nil)
