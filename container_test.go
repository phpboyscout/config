package config_test

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/log"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/phpboyscout/config"
)

type TestObserver struct {
	handler func(config.Containable, chan error)
}

func (o TestObserver) Run(c config.Containable, errs chan error) {
	o.handler(c, errs)
}

// TestContainer_AddObserver provides a convoluted test for triggering multiple observers of filesystem changes.
func TestContainer_AddObserver(t *testing.T) {
	t.Parallel()
	logger := log.New(io.Discard)

	t.Run("with single config file", func(t *testing.T) {
		t.Parallel()
		filename := "/tmp/config.yml"
		f, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
		require.NoError(t, err)

		_, err = f.WriteString(firstMockFilesYaml)
		require.NoError(t, err)

		err = f.Close()
		require.NoError(t, err)

		c := config.NewFilesContainer(logger, afero.NewOsFs(), filename)
		origValue := c.GetString("yaml.key")
		observed := 0

		observeFunc := func(c config.Containable, errors chan error) {
			observed++
			newValue := c.GetString("yaml.key")
			// t.Logf("observed = %d, origValue = %s, newValue = %s", observed, origValue, newValue)
			if origValue == newValue {
				t.Fail()
			}
		}

		c.AddObserver(TestObserver{observeFunc})
		c.AddObserverFunc(observeFunc)

		f2, _ := os.OpenFile(filename, os.O_RDWR|os.O_TRUNC, 0755)
		_, err = f2.WriteString(secondMockFilesYaml)
		require.NoError(t, err)

		err = f2.Close()
		require.NoError(t, err)

		time.Sleep(1 * time.Second)

		assert.Len(t, c.GetObservers(), 2)

		if observed >= 2 && observed%len(c.GetObservers()) != 0 {
			// fsnotify can at times trigger multiple times, so the test accounts for this by testing
			// for the modulus of observations to the number of observers
			t.Errorf("Expected 2 observations, Observed: %d", observed)
		}
	})
}

func TestContainer_Get(t *testing.T) {
	t.Parallel()
	l := log.New(io.Discard)
	c := config.NewReaderContainer(l, "yaml", strings.NewReader(firstMockFilesYaml))

	t.Run("test Get", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "value", c.GetString("yaml.key"))
	})

	t.Run("test GetBool", func(t *testing.T) {
		t.Parallel()
		assert.True(t, c.GetBool("yaml.bool"))
	})

	t.Run("test GetString", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "value", c.GetString("yaml.key"))
	})

	t.Run("test GetInt", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, 1, c.GetInt("yaml.int"))
	})

	t.Run("test GetFloat", func(t *testing.T) {
		t.Parallel()
		assert.InDelta(t, 2.4, c.GetFloat("yaml.float"), 0)
	})

	t.Run("test GetTime", func(t *testing.T) {
		t.Parallel()
		val := c.GetTime("yaml.time")
		expected, _ := time.Parse("2006-01-02 15:04:05", "2021-09-11 12:34:56")

		assert.Equal(t, expected, val)
	})

	t.Run("test GetDuration", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, 5*time.Second, c.GetDuration("yaml.duration"))
	})
}

// func TestContainer_Dump(t *testing.T) {
// 	t.Parallel()

// 	l := log.New(io.Discard)
// 	c := NewReaderContainer(l, "yaml", strings.NewReader(firstMockFilesYaml))
// 	c.Dump()
// }

func TestContainer_Sub(t *testing.T) {
	t.Parallel()

	l := log.New(io.Discard)
	c := config.NewReaderContainer(l, "yaml", strings.NewReader(secondMockFilesYaml))
	s := c.Sub("yaml.more")

	assert.Equal(t, "secondfile", s.GetString("key2"))

}

func TestContainer_GetViper(t *testing.T) {
	t.Parallel()

	l := log.New(io.Discard)
	c := config.NewReaderContainer(l, "yaml", strings.NewReader(firstMockFilesYaml))
	v := c.GetViper()

	assert.Equal(t, "value", v.GetString("yaml.key"))

}
