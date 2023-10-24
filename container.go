package config

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/charmbracelet/log"
	"github.com/fsnotify/fsnotify"
	"github.com/go-errors/errors"
	"github.com/spf13/viper"
)

type Containable interface {
	Get(key string) interface{}
	GetBool(key string) bool
	GetInt(key string) int
	GetFloat(key string) float64
	GetString(key string) string
	GetTime(key string) time.Time
	GetDuration(key string) time.Duration
	GetViper() *viper.Viper
	Has(key string) bool
	Sub(key string) Containable
	AddObserver(o Observable)
	AddObserverFunc(f func(Containable, chan error))
	ToJSON() string
	Dump()
}

// Container container for configuration.
type Container struct {
	ID        string
	viper     *viper.Viper
	logger    *log.Logger
	observers []Observable
}

// Get interface value from config.
func (c *Container) Get(key string) interface{} {
	return c.viper.Get(key)
}

// GetBool get Bool value from config.
func (c *Container) GetBool(key string) bool {
	return c.viper.GetBool(key)
}

// GetInt get Bool value from config.
func (c *Container) GetInt(key string) int {
	return c.viper.GetInt(key)
}

// GetFloat get Float value from config.
func (c *Container) GetFloat(key string) float64 {
	return c.viper.GetFloat64(key)
}

// GetString get string value from config.
func (c *Container) GetString(key string) string {
	return c.viper.GetString(key)
}

// GetTime get time value from config.
func (c *Container) GetTime(key string) time.Time {
	return c.viper.GetTime(key)
}

// GetDuration get duration value from config.
func (c *Container) GetDuration(key string) time.Duration {
	return c.viper.GetDuration(key)
}

// GetViper retrieves the underlying Viper configuration.
func (c *Container) GetViper() *viper.Viper {
	return c.viper
}

// Has retrieves the underlying Viper configuration.
func (c *Container) Has(key string) bool {
	return c.viper.InConfig(key)
}

// Sub returns a subtree of the parent configuration.
func (c *Container) Sub(key string) Containable {
	return &Container{
		ID:        fmt.Sprintf("%s#%s", c.ID, key),
		viper:     c.viper.Sub(key),
		logger:    c.logger,
		observers: make([]Observable, 0),
	}
}

func (c *Container) handleReadFileError(err error) {
	// just use the default value(s) if the config file was not found.
	var pathError *os.PathError
	if errors.As(err, &pathError) {
		c.logger.Warn("could not load config file. Using default values", "stacktrace", errors.Wrap(err, 0).ErrorStack())
	} else if err != nil { // Handle other errors that occurred while reading the config file
		c.logger.Warn(fmt.Sprintf("Could not read the config file (%s)", err), "stacktrace", errors.Wrap(err, 0).ErrorStack())
	}
}

// watchConfig monitor the changes in the config file.
func (c *Container) watchConfig() {
	c.viper.OnConfigChange(func(e fsnotify.Event) {
		c.logger.Infof("Config updated %v", e)
		errs := make(chan error)
		wg := &sync.WaitGroup{}
		for _, o := range c.observers {
			wg.Add(1)
			go func(o Observable, wg *sync.WaitGroup, errs chan error) {
				o.Run(c, errs)
				wg.Done()
			}(o, wg, errs)
		}
		wg.Wait()
	})
	c.viper.WatchConfig()
}

// AddObserver attach observer to trigger on config update.
func (c *Container) AddObserver(o Observable) {
	c.observers = append(c.observers, o)
}

// AddObserverFunc attach function to trigger on config update.
func (c *Container) AddObserverFunc(f func(Containable, chan error)) {
	c.observers = append(c.observers, Observer{f})
}

// GetObservers retrieve all currently attached Observers.
func (c *Container) GetObservers() []Observable {
	return c.observers
}

// Dump return config as json string.
func (c *Container) ToJSON() string {
	s := c.viper.AllSettings()
	bs, err := json.Marshal(s)
	if err != nil {
		c.logger.Fatal("unable to marshal config to YAML", "stacktrace", errors.Wrap(err, 0).ErrorStack())
	}

	return string(bs)
}

func (c *Container) Dump() {
	fmt.Println(c.ToJSON())
}
