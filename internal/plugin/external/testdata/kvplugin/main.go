// Command kvplugin is kest's built-in kv plugin built as an external one,
// with the SDK: the tests run roadrunner/kv against both and expect the same.
package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/berrymore/kest/internal/plugin/kv"
	"github.com/berrymore/kest/internal/registry"
	"github.com/berrymore/kest/pkg/plugin"
)

func main() {
	err := plugin.Serve(func(s plugin.Setup) (plugin.Plugin, error) {
		var cfg kv.Config
		if err := json.Unmarshal(s.Config, &cfg); err != nil {
			return nil, fmt.Errorf("plugin_config: %w", err)
		}
		if err := cfg.Validate(); err != nil {
			return nil, fmt.Errorf("plugin_config: %w", err)
		}
		logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
		return kv.New(&cfg, registry.BuildContext{Name: s.Name, Logger: logger}), nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
