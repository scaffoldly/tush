package shell

import (
	"context"
	"fmt"
	"time"
)

type Shell struct {
	ctx context.Context
}

func New(ctx context.Context) *Shell {
	return &Shell{ctx: ctx}
}

func (s *Shell) Run() int {
	rc := make(chan int, 1)
	go func() {
		// real shell loop; sends its own exit code when done
		fmt.Println("todo: implement shell")
		time.Sleep(10 * time.Second)
		rc <- 0
	}()
	select {
	case <-s.ctx.Done():
		return 130
	case code := <-rc:
		return code
	}
}
