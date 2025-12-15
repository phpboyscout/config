package config

import (
	"bytes"
	"io"
	"log/slog"

	"github.com/go-errors/errors"
	"github.com/spf13/afero"
)

// EmbeddedFileReader abstracts the ReadFile functionality needed from embed.FS
// This interface allows for easier testing by enabling mock implementations.
type EmbeddedFileReader interface {
	ReadFile(name string) ([]byte, error)
}

var (
	ErrNoFilesFound = errors.Errorf("no configuration files found please run init, or provide a config file using the --config flag")
)

func Load(paths []string, fs afero.Fs, logger *slog.Logger, allowEmptyConfig bool) (Containable, error) {
	logger.Debug("Loading configuration")

	loadable := []string{}

	for _, path := range paths {
		if _, err := fs.Stat(path); err == nil {
			loadable = append(loadable, path)
		}
	}

	if !allowEmptyConfig && len(loadable) == 0 {
		return nil, errors.New(ErrNoFilesFound)
	}

	return NewFilesContainer(logger, fs, loadable...), nil
}

func LoadEmbed(paths []string, fs EmbeddedFileReader, logger *slog.Logger) (Containable, error) {
	logger.Debug("Loading embedded configuration")

	configs := []io.Reader{}

	for _, path := range paths {
		config, err := fs.ReadFile(path)
		if err != nil {
			return nil, errors.WrapPrefix(err, "failed to read embedded config file "+path, 0)
		}

		configs = append(configs, bytes.NewReader(config))
	}

	return NewReaderContainer(logger, "yaml", configs...), nil
}
