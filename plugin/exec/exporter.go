// Package exec streams records as JSONL to a subprocess's stdin.
package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

// Exporter pipes records to an external program: one JSON object per line
// on stdin. Nonzero exit is a loud, attributed error (stderr quoted).
type Exporter struct {
	Cmd []string // argv; Cmd[0] resolved via exec.LookPath
}

// Export sends every record from the channel to the subprocess.
func (e *Exporter) Export(ctx context.Context, records <-chan map[string]any) error {
	if len(e.Cmd) == 0 {
		return fmt.Errorf("exec: empty command")
	}
	cmd := exec.CommandContext(ctx, e.Cmd[0], e.Cmd[1:]...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("exec: stdin pipe %q: %w", e.Cmd[0], err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("exec: start %q: %w", e.Cmd[0], err)
	}
	enc := json.NewEncoder(stdin)
	for r := range records {
		if err := enc.Encode(r); err != nil {
			_ = cmd.Process.Kill() //nolint:errcheck // already failing; cleanup only
			drain(records)
			_ = stdin.Close() //nolint:errcheck // already failing; cleanup only
			_ = cmd.Wait()    //nolint:errcheck // encode error is the reported one
			return fmt.Errorf("exec: encode stdin %q: %w", e.Cmd[0], err)
		}
	}
	if err := stdin.Close(); err != nil {
		_ = cmd.Wait() //nolint:errcheck // close error is the reported one
		return fmt.Errorf("exec: close stdin %q: %w", e.Cmd[0], err)
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("exec: %q: %w: %s", e.Cmd[0], err, stderr.String())
	}
	return nil
}

// drain keeps a failed Export from deadlocking a blocking sender: the sink
// keeps sending until it closes the channel, so discard the rest.
func drain(records <-chan map[string]any) {
	go func() {
		for range records {
		}
	}()
}
