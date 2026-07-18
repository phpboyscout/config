package config_test

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/spf13/afero"

	"gitlab.com/phpboyscout/go/config"
)

func ExampleNewReaderContainer() {
	l := slog.New(slog.DiscardHandler)
	yaml := `
log:
  level: debug
server:
  port: 8080
`
	cfg := config.NewReaderContainer(afero.NewMemMapFs(), config.WithLogger(l), config.WithConfigFormat("yaml"), config.WithConfigReaders(strings.NewReader(yaml)))

	fmt.Println("Level:", cfg.GetString("log.level"))
	fmt.Println("Port:", cfg.GetInt("server.port"))
	// Output:
	// Level: debug
	// Port: 8080
}

func ExampleNewSchema() {
	type AppConfig struct {
		Server struct {
			Port int    `config:"server.port" default:"8080"`
			Host string `config:"server.host"`
		}
		Log struct {
			Level string `config:"log.level" enum:"debug,info,warn,error" default:"info"`
		}
		Github struct {
			Token string `config:"github.token" validate:"required"`
		}
	}

	schema, err := config.NewSchema(config.WithStructSchema(AppConfig{}))
	if err != nil {
		fmt.Println("Error:", err)
		return
	}

	_ = schema // Use with container.Validate(schema)
}

func ExampleContainer_Validate() {
	type AppConfig struct {
		Log struct {
			Level string `config:"log.level" enum:"debug,info,warn,error"`
		}
	}

	l := slog.New(slog.DiscardHandler)
	cfg := config.NewReaderContainer(afero.NewMemMapFs(), config.WithLogger(l), config.WithConfigFormat("yaml"), config.WithConfigReaders(strings.NewReader("log:\n  level: verbose\n")))

	schema, _ := config.NewSchema(config.WithStructSchema(AppConfig{}))

	result := cfg.Validate(schema)
	if !result.Valid() {
		fmt.Println(result.Error())
	}
}

func ExampleObserveSection() {
	type ServerSettings struct {
		Port int `mapstructure:"port"`
	}

	cfg := config.NewReaderContainer(
		afero.NewMemMapFs(),
		config.WithLogger(slog.New(slog.DiscardHandler)),
		config.WithConfigFormat("yaml"),
		config.WithConfigReaders(strings.NewReader("server:\n  port: 8080\n")),
	)

	settings, err := config.ObserveSection[ServerSettings](cfg, "server")
	if err != nil {
		fmt.Println("Error:", err)
		return
	}

	fmt.Println("Port:", settings.Current().Port)
	// Output:
	// Port: 8080
}
