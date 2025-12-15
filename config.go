package config

import (
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/spf13/afero"
	"github.com/spf13/viper"
)

func initContainer(l *slog.Logger, fs afero.Fs) *Container {
	c := Container{
		ID:        "",
		viper:     viper.New(),
		logger:    l,
		observers: make([]Observable, 0),
	}

	c.viper.SetFs(fs)
	c.viper.AutomaticEnv()
	c.viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	c.viper.SetTypeByDefaultValue(true)

	return &c
}

// NewFilesContainer Initialise configuration container to read files from the FS.
func NewFilesContainer(l *slog.Logger, fs afero.Fs, configFiles ...string) *Container {
	c := initContainer(l, fs)

	if len(configFiles) > 0 {
		c.ID = configFiles[0]
		c.viper.SetConfigFile(configFiles[0])
		c.handleReadFileError(c.viper.ReadInConfig())
	}

	if len(configFiles) > 1 {
		for _, f := range configFiles[1:] {
			c.ID = fmt.Sprintf("%s;%s", c.ID, f)
			c.viper.SetConfigFile(f)
			c.handleReadFileError(c.viper.MergeInConfig())
		}

		c.logger.Info("Loaded Config")
		c.watchConfig()
	}

	return c
}

// NewReaderContainer Initialise configuration container to read config from ioReader.
func NewReaderContainer(l *slog.Logger, format string, configReaders ...io.Reader) *Container {
	c := initContainer(l, afero.NewOsFs())

	c.viper.SetConfigType(format)

	if len(configReaders) > 0 {
		c.ID = "0"
		c.handleReadFileError(c.viper.ReadConfig(configReaders[0]))
	}

	if len(configReaders) > 1 {
		for i, f := range configReaders[1:] {
			c.ID = fmt.Sprintf("%s;%d", c.ID, i+1)
			c.handleReadFileError(c.viper.MergeConfig(f))
		}

		c.logger.Info("Loaded Config")
	}

	return c
}
