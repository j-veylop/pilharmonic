package orchestrator

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Model a streaming executor and a UI polling task snapshots at 10 Hz.
// Each timed operation writes one 1 KiB chunk through the real output sink.
func BenchmarkStreamingPersistence(b *testing.B) {
	for _, history := range []int{0, 32} {
		b.Run(fmt.Sprintf("history_%d", history), func(b *testing.B) {
			dir := b.TempDir()
			s, err := Open(filepath.Join(dir, "state.json"))
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() {
				if err := s.Shutdown(); err != nil {
					b.Error(err)
				}
			})
			output := strings.Repeat("x", 64<<10)
			for i := range history {
				id := fmt.Sprint(i)
				s.data.Tasks = append(s.data.Tasks, Task{ID: id, Status: Completed, Output: output,
					Attempts: []Attempt{{ID: id, Status: Completed, Output: output}}})
			}
			ready := make(chan io.Writer, 1)
			s.RegisterRunner("pi", runnerFunc(func(ctx context.Context, _ Task, w io.Writer) error {
				ready <- w
				<-ctx.Done()
				return ctx.Err()
			}))
			task, err := s.CreateTask("stream", "benchmark", dir)
			if err != nil {
				b.Fatal(err)
			}
			if err := s.Start(context.Background(), task.ID); err != nil {
				b.Fatal(err)
			}
			w := <-ready
			stop := make(chan struct{})
			done := make(chan struct{})
			go func() {
				defer close(done)
				tick := time.NewTicker(100 * time.Millisecond)
				defer tick.Stop()
				for {
					select {
					case <-stop:
						return
					case <-tick.C:
						s.ListTasks()
					}
				}
			}()
			defer func() { close(stop); <-done }()
			chunk := []byte(strings.Repeat("a", 1024))
			b.ReportAllocs()
			b.SetBytes(int64(len(chunk)))
			for b.Loop() {
				if _, err := w.Write(chunk); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
