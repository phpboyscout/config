package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"gitlab.com/phpboyscout/go/config"
	"gitlab.com/phpboyscout/go/config/mocks"
)

type serverConfig struct {
	Host string `config:"host" validate:"required"`
	Port int    `config:"port"`
}

// TestValidateStruct_AcceptsAMock is the reason ValidateStruct takes Reader
// rather than *View.
//
// Downstream code generators emit a Validate<Name>Config wrapper around this
// call. With a concrete *View parameter, every generated wrapper — in every
// scaffolded tool — could only be exercised by standing up a real Store and
// real files. Validation only reads, so the narrow type bought nothing and cost
// every consumer their own testability.
//
// These tests live in config_test rather than beside the other validation tests
// because mocks imports config, so the internal package cannot import mocks.
func TestValidateStruct_AcceptsAMock(t *testing.T) {
	t.Parallel()

	reader := mocks.NewMockReader(t)
	reader.EXPECT().Get("host").Return("localhost").Maybe()
	reader.EXPECT().Has("host").Return(true).Maybe()
	reader.EXPECT().Get("port").Return(8080).Maybe()
	reader.EXPECT().Has("port").Return(true).Maybe()
	reader.EXPECT().Keys().Return([]string{"host", "port"}).Maybe()
	reader.EXPECT().Shadowed(mock.Anything).Return(nil).Maybe()

	require.NoError(t, config.ValidateStruct[serverConfig](reader))
}

// TestValidateStruct_AMockCanFailValidationToo keeps the test above honest.
//
// A mock that satisfies the interface but is never actually consulted would
// pass it just as well, so this asserts the mocked values genuinely drive the
// outcome rather than merely being accepted.
func TestValidateStruct_AMockCanFailValidationToo(t *testing.T) {
	t.Parallel()

	reader := mocks.NewMockReader(t)
	reader.EXPECT().Get(mock.Anything).Return(nil).Maybe()
	reader.EXPECT().Has(mock.Anything).Return(false).Maybe()
	reader.EXPECT().Keys().Return(nil).Maybe()
	reader.EXPECT().Shadowed(mock.Anything).Return(nil).Maybe()

	err := config.ValidateStruct[serverConfig](reader)

	require.ErrorIs(t, err, config.ErrInvalidConfig)
	assert.Contains(t, err.Error(), "host", "the failure must name the missing key")
}

// TestValidateStruct_NilReaderDoesNotPanic covers the guard that quietly
// changed meaning when this parameter became an interface.
//
// A plain view == nil caught a nil *View while the parameter was concrete.
// Boxed in a Reader the same comparison is false, because the interface carries
// a type — so without an explicit reflect-based check the nil pointer reaches
// Get and panics, on precisely the input the guard was written to handle.
// Reverting isNil to view == nil fails this test.
func TestValidateStruct_NilReaderDoesNotPanic(t *testing.T) {
	t.Parallel()

	var nilView *config.View

	assert.NotPanics(t, func() {
		// A typed nil: non-nil interface, nil pointer inside.
		_ = config.ValidateStruct[serverConfig](nilView)
	}, "a nil *View boxed in a Reader must not reach the accessors")

	assert.NotPanics(t, func() {
		_ = config.ValidateStruct[serverConfig](nil)
	}, "an entirely absent Reader must not panic either")
}

// TestValidateStruct_StillTakesAView pins the source compatibility the release
// depends on: widening the parameter must not break the callers that were
// already passing a real view.
func TestValidateStruct_StillTakesAView(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, config.OS().WriteFile(dir+"/app.yaml",
		[]byte("host: localhost\nport: 8080\n"), 0o600))

	store, err := config.NewStore(t.Context(),
		config.WithFiles(config.OS(), dir+"/app.yaml"))
	require.NoError(t, err)

	require.NoError(t, config.ValidateStruct[serverConfig](store.View()))
}
