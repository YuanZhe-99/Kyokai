//go:build android

package gui

import (
	"sync"
	"sync/atomic"

	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/core"
)

// sshClient holds the shared SFTP/SCP panel fields on Android while
// mobileTermSession drives the SSH terminal view directly.
type sshClient struct {
	s       *serviceTab
	conn    *sshTerm
	pic     ui.Handle
	windows bool
	title   atomic.Pointer[string]
	files   atomic.Pointer[remoteFiles]
	opening sync.Once
	panel   filesPanel
}

func (c *sshClient) shownTitle() string {
	if t := c.title.Load(); t != nil {
		return *t
	}
	return ""
}

func (c *sshClient) remote() *remoteFiles {
	c.opening.Do(func() {
		go func() {
			c.files.Store(openRemoteFiles(c.conn.client, c.windows, c.s.redraw))
			c.s.redraw()
		}()
	})
	return c.files.Load()
}

func (c *sshClient) view(*ui.Context, *serviceTab) {}

func (c *sshClient) close() {
	if f := c.files.Load(); f != nil {
		f.close()
	}
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

func (c *sshClient) grabs() bool        { return false }
func (c *sshClient) scan(core.KeyEvent) {}
func (c *sshClient) setFront(bool)      {}
func (c *sshClient) keys() *ui.Handle   { return &c.pic }
func (c *sshClient) sendInput(p []byte) {
	if c.conn != nil {
		_, _ = c.conn.Write(p)
	}
}

func (a *App) setTermLook(string, TermLook)           {}
func termPreview(*ui.Context, string, TermLook, bool) {}

func termLookForm(_ *ui.Context, _ string, l TermLook, _ *TermLook, _ func(string, string, func())) (TermLook, bool) {
	return l, false
}
