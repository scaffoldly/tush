// Package progress tells the user what is happening while it happens.
//
// Publishing a tunnel takes long enough that silence is indistinguishable from
// a hang, so the steps that wait say so.
package progress

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/term"
)

// frames animate a step in place. Braille dots because they occupy one column
// and do not jump around as the animation cycles.
var frames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// tick is how often the animation advances.
const tick = 100 * time.Millisecond

// Reporter announces steps to a stream. On a terminal a step animates in place
// and leaves nothing behind; anywhere else — a pipe, a log, CI — it prints one
// plain line, because animation frames in a log file are noise.
type Reporter struct {
	w       io.Writer
	animate bool

	mu      sync.Mutex
	stop    chan struct{}
	stopped sync.WaitGroup
	width   int
}

func New(w io.Writer) *Reporter {
	return &Reporter{w: w, animate: isTerminal(w)}
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// Step announces work that is starting, ending whatever step preceded it. On a
// terminal it keeps animating until the next Step or a Stop.
//
// The message is printed as given — including any trailing ellipsis, which is
// the caller's to write, so that the animated and plain forms read alike.
func (r *Reporter) Step(message string) {
	r.Stop()

	if !r.animate {
		fmt.Fprintf(r.w, "%s\n", message)
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.stop = make(chan struct{})
	r.width = len(message) + 2
	r.stopped.Add(1)

	go func(stop <-chan struct{}) {
		defer r.stopped.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			fmt.Fprintf(r.w, "\r%c %s", frames[i%len(frames)], message)
			select {
			case <-stop:
				return
			case <-time.After(tick):
			}
		}
	}(r.stop)
}

// Stop ends the current step, leaving the line as it was before it started.
// Calling it without a step running does nothing.
func (r *Reporter) Stop() {
	r.mu.Lock()
	stop, width := r.stop, r.width
	r.stop = nil
	r.mu.Unlock()

	if stop == nil {
		return
	}
	close(stop)
	r.stopped.Wait()

	// Blank the line rather than leaving a finished step on screen: what
	// follows says what happened, and a stale spinner frame would sit above it.
	fmt.Fprintf(r.w, "\r%*s\r", width, "")
}
