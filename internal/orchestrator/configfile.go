package orchestrator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// maxConfigSize bounds every operator-supplied JSON configuration file so a
// mistaken path (a disk image, a log) cannot be read into memory.
const maxConfigSize = 1 << 20

// readConfigFile reads at most maxConfigSize bytes, reporting an oversized file
// rather than truncating it. The label names the file in error messages.
func readConfigFile(path, label string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	body, err := io.ReadAll(io.LimitReader(f, maxConfigSize+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxConfigSize {
		return nil, fmt.Errorf("%s config exceeds 1 MiB", label)
	}
	return body, nil
}

// decodeConfigFile decodes exactly one JSON object into dst, rejecting unknown
// fields and trailing content so a typo fails loudly instead of being ignored.
func decodeConfigFile(path, label string, dst any) error {
	body, err := readConfigFile(path, label)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("%s config: %w", label, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("%s config must contain exactly one JSON object", label)
	}
	return nil
}
