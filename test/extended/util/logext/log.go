package logext

/*
  author: rioliu@redhat.com
*/

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/rs/zerolog"
)

const (
	// EnableDebugLog env variable to enable debug logging
	EnableDebugLog = "GINKGO_TEST_ENABLE_DEBUG_LOG"
)

// ginkgoWriterWrapper wrapper interface for zerolog
type ginkgoWriterWrapper struct {
}

// Write is a wrapper to redirect all logs to the current Ginkgo writer.
// The Ginkgo writer may change at any given time (e.g. the OTE framework changes it between tests) so it's required
// to always point to the global GinkgoWriter variable to write to the current writer and not an old one.
func (w *ginkgoWriterWrapper) Write(p []byte) (n int, err error) {
	return ginkgo.GinkgoWriter.Write(p)
}

var logger zerolog.Logger
var loggerOnce sync.Once

// getLogger initializes the logger variable as a singleton based on a zerolog logger
// default log level is INFO, user can enable debug logging by env variable GINKGO_TEST_ENABLE_DEBUG_LOG
func getLogger() *zerolog.Logger {
	loggerOnce.Do(func() {
		// customize time field format to sync with e2e framework
		zerolog.TimeFieldFormat = time.StampMilli
		// initialize customized output to integrate with GinkgoWriter
		output := zerolog.ConsoleWriter{
			Out:        &ginkgoWriterWrapper{},
			TimeFormat: time.StampMilli,
			NoColor:    true,
		}
		// customize level format e.g. INFO, DEBUG, ERROR
		output.FormatLevel = func(i interface{}) string {
			return strings.ToUpper(fmt.Sprintf("%s:", i))
		}
		// disable colorful output for timestamp field
		output.FormatTimestamp = func(i interface{}) string {
			return fmt.Sprintf("%s:", i)
		}
		logger = zerolog.New(output).With().Timestamp().Logger()
		// set default log level to INFO
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
		// if system env var is defined, enable debug logging
		if _, enabled := os.LookupEnv(EnableDebugLog); enabled {
			zerolog.SetGlobalLevel(zerolog.DebugLevel)
		}
	})
	return &logger
}

// Infof log info level message
func Infof(format string, v ...interface{}) {
	getLogger().Info().Msgf(format, v...)
}

// Debugf log debug level message
func Debugf(format string, v ...interface{}) {
	getLogger().Debug().Msgf(format, v...)
}

// Errorf log error level message
func Errorf(format string, v ...interface{}) {
	getLogger().Error().Msgf(format, v...)
}

// Warnf log warning level message
func Warnf(format string, v ...interface{}) {
	getLogger().Warn().Msgf(format, v...)
}
