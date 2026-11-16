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
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// ErrDeadlinesNotSupported is returned when a deadline method is invoked on a stdio connection.
var ErrDeadlinesNotSupported = errors.New("stdio: deadlines not supported")

// closeGracePeriod is the time allowed for a child process to exit cleanly
// after its pipes are closed before forcefully terminating it.
const closeGracePeriod = 2 * time.Second

type stdioAddr string

func (address stdioAddr) Network() string { return string(address) }
func (address stdioAddr) String() string  { return string(address) }

type baseConn struct{}

// LocalAddr returns the local network address for the stdio connection.
func (baseConn) LocalAddr() net.Addr { return stdioAddr("stdio") }

// RemoteAddr returns the remote network address for the stdio connection.
func (baseConn) RemoteAddr() net.Addr { return stdioAddr("stdio") }

// SetDeadline returns ErrDeadlinesNotSupported because stdio streams do not support deadlines.
func (baseConn) SetDeadline(time.Time) error { return ErrDeadlinesNotSupported }

// SetReadDeadline returns ErrDeadlinesNotSupported because stdio streams do not support deadlines.
func (baseConn) SetReadDeadline(time.Time) error { return ErrDeadlinesNotSupported }

// SetWriteDeadline returns ErrDeadlinesNotSupported because stdio streams do not support deadlines.
func (baseConn) SetWriteDeadline(time.Time) error { return ErrDeadlinesNotSupported }

// Conn wraps an io.ReadCloser and io.WriteCloser into a net.Conn.
type Conn struct {
	baseConn
	reader     io.ReadCloser
	writer     io.WriteCloser
	closeOnce  sync.Once
	closed     atomic.Bool
	closeError error
}

// NewConn returns a net.Conn over an io.ReadCloser and io.WriteCloser.
func NewConn(reader io.ReadCloser, writer io.WriteCloser) *Conn {
	return &Conn{
		reader: reader,
		writer: writer,
	}
}

// Read reads data from the underlying reader into buffer.
func (connection *Conn) Read(buffer []byte) (int, error) {
	if connection.closed.Load() {
		return 0, net.ErrClosed
	}
	return connection.reader.Read(buffer)
}

// Write writes data from buffer to the underlying writer.
func (connection *Conn) Write(buffer []byte) (int, error) {
	if connection.closed.Load() {
		return 0, net.ErrClosed
	}
	return connection.writer.Write(buffer)
}

// Close closes both the underlying reader and writer.
func (connection *Conn) Close() error {
	connection.closed.Store(true)
	connection.closeOnce.Do(func() {
		connection.closeError = errors.Join(
			connection.reader.Close(),
			connection.writer.Close(),
		)
	})
	return connection.closeError
}

var _ net.Conn = (*Conn)(nil)

// Listener implements net.Listener for the plugin side, yielding one
// connection over stdin and stdout and then blocking until closed.
type Listener struct {
	yield     chan net.Conn
	done      chan struct{}
	closeOnce sync.Once
}

type listenerConn struct {
	*Conn
	listener *Listener
}

func (connection *listenerConn) Close() error {
	return errors.Join(
		connection.listener.Close(),
		connection.Conn.Close(),
	)
}

// NewListener returns a net.Listener that yields a single net.Conn over reader and writer.
func NewListener(reader io.ReadCloser, writer io.WriteCloser) *Listener {
	listener := &Listener{
		yield: make(chan net.Conn, 1),
		done:  make(chan struct{}),
	}
	listenerConnection := &listenerConn{
		Conn:     NewConn(reader, writer),
		listener: listener,
	}
	listener.yield <- listenerConnection
	return listener
}

// Accept yields the single connection over reader and writer on first call.
// Subsequent calls block until the listener is closed, then return net.ErrClosed.
func (listener *Listener) Accept() (net.Conn, error) {
	select {
	case <-listener.done:
		return nil, net.ErrClosed
	default:
	}
	select {
	case <-listener.done:
		return nil, net.ErrClosed
	case connection := <-listener.yield:
		return connection, nil
	}
}

// Close closes the listener, unblocking any blocked Accept calls.
func (listener *Listener) Close() error {
	listener.closeOnce.Do(func() {
		close(listener.done)
	})
	return nil
}

// Addr returns the stdio network address.
func (listener *Listener) Addr() net.Addr {
	return stdioAddr("stdio")
}

var _ net.Listener = (*Listener)(nil)

// CommandConn implements net.Conn for the host (ap) side over a started command's
// stdin and stdout pipes. Closing it closes the pipes and waits a bounded duration
// for the command to exit, killing it if it does not exit promptly. A command that
// exits early turns pending reads and writes into errors carrying the exit status.
type CommandConn struct {
	baseConn
	command    *exec.Cmd
	stdinPipe  io.WriteCloser
	stdoutPipe io.ReadCloser
	closeOnce  sync.Once
	closeError error
	waitOnce   sync.Once
	waitDone   chan struct{}
	waitError  error
	closed     atomic.Bool
}

// NewCommandConn returns a net.Conn over a started command's stdin and stdout pipes.
func NewCommandConn(command *exec.Cmd, stdinPipe io.WriteCloser, stdoutPipe io.ReadCloser) *CommandConn {
	return &CommandConn{
		command:    command,
		stdinPipe:  stdinPipe,
		stdoutPipe: stdoutPipe,
		waitDone:   make(chan struct{}),
	}
}

func (commandConnection *CommandConn) startWait() {
	commandConnection.waitOnce.Do(func() {
		commandConnection.waitError = commandConnection.command.Wait()
		close(commandConnection.waitDone)
	})
}

func (commandConnection *CommandConn) wait() error {
	commandConnection.startWait()
	<-commandConnection.waitDone
	return commandConnection.waitError
}

// Read reads bytes from the child process's stdout pipe into buffer.
// If the child process exits early, pending and subsequent reads return
// an error carrying the child's exit status.
func (commandConnection *CommandConn) Read(buffer []byte) (int, error) {
	if commandConnection.closed.Load() {
		return 0, net.ErrClosed
	}
	bytesRead, err := commandConnection.stdoutPipe.Read(buffer)
	if err != nil {
		if commandConnection.closed.Load() {
			return bytesRead, net.ErrClosed
		}
		if bytesRead > 0 && err == io.EOF {
			return bytesRead, nil
		}
		if waitError := commandConnection.wait(); waitError != nil {
			return bytesRead, waitError
		}
		return bytesRead, err
	}
	return bytesRead, nil
}

// Write writes bytes from buffer to the child process's stdin pipe.
// If the child process exits early, pending and subsequent writes return
// an error carrying the child's exit status.
func (commandConnection *CommandConn) Write(buffer []byte) (int, error) {
	if commandConnection.closed.Load() {
		return 0, net.ErrClosed
	}
	bytesWritten, err := commandConnection.stdinPipe.Write(buffer)
	if err != nil {
		if commandConnection.closed.Load() {
			return bytesWritten, net.ErrClosed
		}
		if waitError := commandConnection.wait(); waitError != nil {
			return bytesWritten, waitError
		}
		return bytesWritten, err
	}
	return bytesWritten, nil
}

// Close closes the child process's stdin pipe, waits a bounded duration
// for the child process to exit (killing it if it does not exit promptly),
// and then closes the stdout pipe.
func (commandConnection *CommandConn) Close() error {
	commandConnection.closed.Store(true)
	commandConnection.closeOnce.Do(func() {
		var closeErrors []error
		if err := commandConnection.stdinPipe.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			closeErrors = append(closeErrors, err)
		}

		go commandConnection.startWait()

		select {
		case <-commandConnection.waitDone:
			closeErrors = append(closeErrors, commandConnection.waitError)
		case <-time.After(closeGracePeriod):
			killError := commandConnection.command.Process.Kill()
			<-commandConnection.waitDone
			if killError != nil && !errors.Is(killError, os.ErrProcessDone) {
				closeErrors = append(closeErrors, fmt.Errorf("child process did not exit within %v and kill failed: %w (exit: %v)", closeGracePeriod, killError, commandConnection.waitError))
			} else {
				closeErrors = append(closeErrors, fmt.Errorf("child process did not exit within %v and was killed: %w", closeGracePeriod, commandConnection.waitError))
			}
		}
		if err := commandConnection.stdoutPipe.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			closeErrors = append(closeErrors, err)
		}
		commandConnection.closeError = errors.Join(closeErrors...)
	})
	return commandConnection.closeError
}

var _ net.Conn = (*CommandConn)(nil)
